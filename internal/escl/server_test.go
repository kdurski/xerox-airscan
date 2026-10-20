package escl

import (
	"context"
	"encoding/xml"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"xerox-airscan/internal/xerox"
)

func TestFeederBatchAndEndOfPaper(t *testing.T) {
	s := New("unused:9400", testInfo, true)
	defer s.Close()
	// The entire batch exceeds this timeout, while each page is shorter.
	s.Timeout = 200 * time.Millisecond
	count := 0
	s.scanPage = func(ctx context.Context, job *job) (image.Image, error) {
		settings := job.settings
		if settings.Source != "Feeder" {
			t.Errorf("wrong input source: %s", settings.Source)
		}
		count++
		if count > 2 {
			return nil, xerox.ErrNoPaper
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
		img := image.NewGray(image.Rect(0, 0, 10, 12))
		img.SetGray(0, 0, color.Gray{Y: uint8(count * 60)})
		return img, nil
	}
	created := call(s, "POST", "/eSCL/ScanJobs", strings.Replace(request, ">Platen<", ">Feeder<", 1))
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body)
	}
	location := created.Header().Get("Location")
	for n := 1; n <= 2; n++ {
		w := call(s, "GET", location+"/NextDocument", "")
		if w.Code != 200 {
			t.Fatalf("page %d: %d %s", n, w.Code, w.Body)
		}
		img, err := png.Decode(w.Body)
		if err != nil {
			t.Fatal(err)
		}
		if color.GrayModel.Convert(img.At(0, 0)).(color.Gray).Y != uint8(n*60) {
			t.Fatalf("wrong page %d", n)
		}
		if got := call(s, "POST", "/eSCL/ScanJobs", request).Code; got != 503 {
			t.Fatalf("batch lost ownership: %d", got)
		}
	}
	if w := call(s, "GET", location+"/NextDocument", ""); w.Code != 410 {
		t.Fatalf("end of batch: %d %s", w.Code, w.Body)
	}
	s.mu.Lock()
	if s.active != nil {
		t.Error("finished batch still owns scanner")
	}
	for _, j := range s.jobs {
		if j.state != "Completed" || j.pages != 2 {
			t.Errorf("bad final state: %+v", j)
		}
	}
	s.mu.Unlock()
	if got := call(s, "GET", location+"/NextDocument", "").Code; got != 410 {
		t.Fatal(got)
	}
	if count != 3 {
		t.Fatalf("finished job scanned again: %d", count)
	}
	if got := call(s, "POST", "/eSCL/ScanJobs", request).Code; got != 201 {
		t.Fatalf("scanner not released: %d", got)
	}
}

func TestFeederFailuresDoNotCompleteBatch(t *testing.T) {
	for name, scanErr := range map[string]error{"empty": xerox.ErrNoPaper, "jam": xerox.ErrPaperJam, "cover": xerox.ErrCoverOpen, "truncated": errors.New("missing final strip")} {
		t.Run(name, func(t *testing.T) {
			s := New("unused:9400", testInfo, true)
			defer s.Close()
			s.scanPage = func(context.Context, *job) (image.Image, error) { return nil, scanErr }
			created := call(s, "POST", "/eSCL/ScanJobs", strings.Replace(request, ">Platen<", ">Feeder<", 1))
			w := call(s, "GET", created.Header().Get("Location")+"/NextDocument", "")
			want := 502
			if name == "empty" {
				want = 410
			}
			if w.Code != want {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			for _, j := range s.jobs {
				if j.state != "Aborted" || j.pages != 0 {
					t.Fatalf("failed job marked successful: %+v", j)
				}
			}
			if s.active != nil {
				t.Fatal("failed job retained scanner")
			}
		})
	}
}

func TestLiveFeederPaperStatus(t *testing.T) {
	s := New("unused:9400", testInfo, true)
	defer s.Close()
	loaded := true
	probes := 0
	s.probe = func(context.Context) (xerox.Info, error) {
		probes++
		i := testInfo
		i.PaperLoaded = loaded
		return i, nil
	}
	if w := call(s, "GET", "/eSCL/ScannerStatus", ""); !strings.Contains(w.Body.String(), "ScannerAdfLoaded") {
		t.Fatal(w.Body)
	}
	loaded = false
	s.statusChecked = time.Time{}
	if w := call(s, "GET", "/eSCL/ScannerStatus", ""); !strings.Contains(w.Body.String(), "ScannerAdfEmpty") {
		t.Fatal(w.Body)
	}
	call(s, "GET", "/eSCL/ScannerStatus", "")
	if probes != 2 {
		t.Fatal("status did not cache probe")
	}
	call(s, "POST", "/eSCL/ScanJobs", request)
	s.statusChecked = time.Time{}
	call(s, "GET", "/eSCL/ScannerStatus", "")
	if probes != 2 {
		t.Fatal("status queried printer during an active scan job")
	}
}

var testInfo = xerox.Info{Model: "Xerox WorkCentre 3225", Width: 10208, PlatenHeight: 14040, ADFHeight: 16800, Resolutions: []int{75, 100, 150, 200, 300, 600}}

const request = `<scan:ScanSettings xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03" xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm"><pwg:Version>2.0</pwg:Version><scan:ScanRegions><scan:ScanRegion><pwg:ContentRegionUnits>escl:ThreeHundredthsOfInches</pwg:ContentRegionUnits><pwg:XOffset>16</pwg:XOffset><pwg:YOffset>24</pwg:YOffset><pwg:Width>2400</pwg:Width><pwg:Height>3200</pwg:Height></scan:ScanRegion></scan:ScanRegions><pwg:InputSource>Platen</pwg:InputSource><scan:ColorMode>RGB24</scan:ColorMode><scan:XResolution>75</scan:XResolution><scan:YResolution>75</scan:YResolution><pwg:DocumentFormat>image/png</pwg:DocumentFormat></scan:ScanSettings>`

func TestParseImageCaptureSettings(t *testing.T) {
	s, err := ParseSettings(strings.NewReader(request), testInfo, false)
	if err != nil {
		t.Fatal(err)
	}
	if s.X != 16 || s.Y != 24 || s.Width != 2400 || s.Height != 3200 || s.DPI != 75 || s.Format != "image/png" {
		t.Fatalf("%+v", s)
	}
}

func TestRejectInvalidSettings(t *testing.T) {
	for name, body := range map[string]string{
		"out of bounds":          strings.Replace(request, "<pwg:Width>2400", "<pwg:Width>3000", 1),
		"negative offset":        strings.Replace(request, "<pwg:XOffset>16", "<pwg:XOffset>-1", 1),
		"unequal resolutions":    strings.Replace(request, "<scan:YResolution>75", "<scan:YResolution>300", 1),
		"unsupported resolution": strings.ReplaceAll(request, ">75<", ">76<"),
		"format":                 strings.Replace(request, "image/png", "image/pdf", 1),
		"color":                  strings.Replace(request, "RGB24", "RGB48", 1),
		"feeder disabled":        strings.Replace(request, ">Platen<", ">Feeder<", 1),
		"duplex":                 strings.Replace(request, "</scan:ScanSettings>", "<scan:Duplex>true</scan:Duplex></scan:ScanSettings>", 1),
		"extra document":         request + "<evil/>",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSettings(strings.NewReader(body), testInfo, false); err == nil {
				t.Fatal("accepted invalid settings")
			}
		})
	}
}

func call(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestHTTPJobLifecycle(t *testing.T) {
	s := New("192.0.2.1:9400", testInfo, false)
	defer s.Close()
	w := call(s, "POST", "http://scanner.local:8090/eSCL/ScanJobs", request)
	if w.Code != 201 || !strings.HasPrefix(w.Header().Get("Location"), "http://scanner.local:8090/eSCL/ScanJobs/") {
		t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
	}
	location := w.Header().Get("Location")
	if got := call(s, "POST", "/eSCL/ScanJobs", request).Code; got != 503 {
		t.Fatalf("concurrent job: %d", got)
	}
	w = call(s, "GET", "/eSCL/ScannerStatus", "")
	if !strings.Contains(w.Body.String(), "Processing") {
		t.Fatal(w.Body)
	}
	if got := call(s, "DELETE", location, "").Code; got != 200 {
		t.Fatalf("cancel: %d", got)
	}
	if got := call(s, "GET", location+"/NextDocument", "").Code; got != 410 {
		t.Fatalf("canceled document: %d", got)
	}
	if got := call(s, "POST", "/eSCL/ScanJobs", request).Code; got != 201 {
		t.Fatalf("new job after cancel: %d", got)
	}
	if got := call(s, "GET", "/eSCL/ScanJobs/unknown/NextDocument", "").Code; got != 404 {
		t.Fatalf("unknown: %d", got)
	}
}

func TestUnconsumedJobsExpire(t *testing.T) {
	s := New("192.0.2.1:9400", testInfo, false)
	defer s.Close()
	s.Timeout = time.Millisecond
	if got := call(s, "POST", "/eSCL/ScanJobs", request).Code; got != 201 {
		t.Fatal(got)
	}
	time.Sleep(5 * time.Millisecond)
	if got := call(s, "POST", "/eSCL/ScanJobs", request).Code; got != 201 {
		t.Fatalf("expired job still blocks scanner: %d", got)
	}
}

func TestCapabilitiesAreWellFormed(t *testing.T) {
	for _, adf := range []bool{false, true} {
		s := New("printer:9400", testInfo, adf)
		w := call(s, "GET", "/eSCL/ScannerCapabilities", "")
		d := xml.NewDecoder(w.Body)
		resolutions := 0
		for {
			tok, err := d.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if start, ok := tok.(xml.StartElement); ok && start.Name.Local == "DiscreteResolution" {
				resolutions++
			}
		}
		want := 6
		if adf {
			want = 12
		}
		if resolutions != want {
			t.Fatalf("%d resolutions", resolutions)
		}
	}
}
