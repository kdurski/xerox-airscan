package xerox

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"net"
	"testing"
	"time"
)

func TestCaptureReconstructsCompleteImage(t *testing.T) {
	e := fixture(t)
	img, info, bands, err := CaptureImage(e)
	if err != nil {
		t.Fatal(err)
	}
	if len(e) != 79 || bands != 28 || img.Bounds() != image.Rect(0, 0, 638, 880) {
		t.Fatalf("capture changed: %d commands, %d bands, %v", len(e), bands, img.Bounds())
	}
	if info.Width != 10208 || info.PlatenHeight != 14040 || info.ADFHeight != 16800 || len(info.Resolutions) != 6 {
		t.Fatalf("bad identity: %+v", info)
	}
}

func TestWindowMatchesVendorCapture(t *testing.T) {
	s := Settings{DPI: 75, Width: 2552, Height: 4200, Source: "Platen"}
	for _, e := range fixture(t) {
		if e.Command[2] == 0x24 {
			if !bytes.Equal(Window(s), e.Command) {
				t.Fatalf("got %x want %x", Window(s), e.Command)
			}
			return
		}
	}
	t.Fatal("missing window command")
}

func TestExactSoftwareCropAcrossBands(t *testing.T) {
	a := NewAssembler(Settings{DPI: 300, X: 2, Y: 2, Width: 4, Height: 4, Gray: true})
	for n := 0; n < 2; n++ {
		img := image.NewGray(image.Rect(0, 0, 8, 4))
		for y := 0; y < 4; y++ {
			for x := 0; x < 8; x++ {
				img.SetGray(x, y, color.Gray{Y: uint8((y + 4*n) * 25)})
			}
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 100}); err != nil {
			t.Fatal(err)
		}
		data := append([]byte{0, 0, 0, 0}, b.Bytes()...)
		data = append(data, make([]byte, 16)...)
		if err := a.Add(Band{Data: data, Width: 8, Height: 4}); err != nil {
			t.Fatal(err)
		}
	}
	for y := 0; y < 4; y++ {
		got := color.GrayModel.Convert(a.Image.At(0, y)).(color.Gray).Y
		want := (y + 2) * 25
		if int(got) < want-1 || int(got) > want+1 {
			t.Fatalf("row %d = %d want %d", y, got, want)
		}
	}
}

func TestFeederAcceptsActualPageLength(t *testing.T) {
	for _, gray := range []bool{false, true} {
		a := NewAssembler(Settings{DPI: 300, X: 4, Y: 4, Width: 20, Height: 100, Gray: gray})
		a.row = 80
		img, err := a.Finish("Feeder")
		if err != nil || img.Bounds() != image.Rect(0, 0, 20, 76) {
			t.Fatalf("short feeder page: %v %v", img, err)
		}
		if _, err = a.Finish("Platen"); err == nil {
			t.Fatal("flatbed truncation was accepted")
		}
		a.row = 4
		if _, err = a.Finish("Feeder"); err == nil {
			t.Fatal("accepted a page ending before the crop")
		}
	}
}

func TestFeederWindow(t *testing.T) {
	s := Settings{DPI: 300, Width: 2552, Height: 4200, Source: "Feeder"}
	want, _ := hex.DecodeString("1ba8241330000027e0000041a0050500000000050601022000")
	if !bytes.Equal(Window(s), want) {
		t.Fatalf("feeder window: %x", Window(s))
	}
}

func TestRejectMalformedJPEG(t *testing.T) {
	for _, data := range [][]byte{nil, {0, 0, 1, 2}, {1, 0xff, 0xd8}, {0xff, 0xd8, 0xff, 0xd9}} {
		if _, err := DecodeBand(Band{Data: data, Width: 638, Height: 32}); err == nil {
			t.Fatalf("accepted %x", data)
		}
	}
}

func TestTCPRetransmissionsAndGaps(t *testing.T) {
	data, err := joinSegments([]segment{{seq: 103, data: []byte("def")}, {seq: 100, data: []byte("abcd")}, {seq: 103, data: []byte("def")}})
	if err != nil || string(data) != "abcdef" {
		t.Fatalf("%q %v", data, err)
	}
	if _, err := joinSegments([]segment{{seq: 100, data: []byte("a")}, {seq: 102, data: []byte("b")}}); err == nil {
		t.Fatal("accepted gap")
	}
	if _, err := joinSegments([]segment{{seq: 100, data: []byte("abcd")}, {seq: 103, data: []byte("xef")}}); err == nil {
		t.Fatal("accepted conflicting retransmission")
	}
}

func TestScanOverFragmentedTCP(t *testing.T) {
	exchanges := fixture(t)
	var inquiry, reserve, window, position, release []byte
	var reads, images [][]byte
	for _, e := range exchanges {
		switch e.Command[2] {
		case 0x12:
			inquiry = e.Response
		case 0x16:
			reserve = e.Response
		case 0x24:
			window = e.Response
		case 0x31:
			if position == nil {
				position = e.Response
			}
		case 0x17:
			release = e.Response
		case 0x28:
			if e.Response[1] == 0 {
				reads = append(reads, e.Response)
			}
		case 0x29:
			images = append(images, e.Response)
		}
	}
	clientConn, scannerConn := net.Pipe()
	done := make(chan error, 1)
	go func() {
		defer scannerConn.Close()
		ri, ii := 0, 0
		for {
			var header [4]byte
			if _, err := io.ReadFull(scannerConn, header[:]); err != nil {
				done <- err
				return
			}
			var response []byte
			switch header[2] {
			case 0x12:
				response = inquiry
			case 0x16:
				response = reserve
			case 0x24:
				rest := make([]byte, 21)
				if _, err := io.ReadFull(scannerConn, rest); err != nil {
					done <- err
					return
				}
				if hex.EncodeToString(rest) != "30000027e0000036d8000000000000050601024000" {
					done <- fmt.Errorf("unexpected window %x", rest)
					return
				}
				response = window
			case 0x31:
				response = position
			case 0x28:
				if ri >= len(reads) {
					done <- fmt.Errorf("too many READ commands")
					return
				}
				response = reads[ri]
				ri++
			case 0x29:
				if ii >= len(images) {
					done <- fmt.Errorf("too many READ_IMAGE commands")
					return
				}
				response = images[ii]
				ii++
			case 0x06:
				done <- fmt.Errorf("successful page was aborted instead of released")
				return
			case 0x17:
				response = release
			default:
				done <- fmt.Errorf("unexpected command %x", header)
				return
			}
			// Fragment independently of command and JPEG boundaries.
			for len(response) > 0 {
				n := min(len(response), 127)
				if _, err := scannerConn.Write(response[:n]); err != nil {
					done <- err
					return
				}
				response = response[n:]
			}
			if header[2] == 0x17 {
				done <- nil
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := &Client{conn: clientConn, ctx: ctx, timeout: 2 * time.Second}
	defer c.Close()
	img, err := c.Scan(Settings{DPI: 75, Width: 2552, Height: 3510, Source: "Platen", Format: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds() != image.Rect(0, 0, 638, 877) {
		t.Fatal(img.Bounds())
	}
	c.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCloseAbortsOnlyIncompletePages(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			a, b := net.Pipe()
			defer b.Close()
			done := make(chan error, 1)
			want := []byte{0x17}
			if !complete {
				want = []byte{0x06, 0x17}
			}
			go func() {
				for _, cmd := range want {
					var request [4]byte
					if _, err := io.ReadFull(b, request[:]); err != nil {
						done <- err
						return
					}
					if request[2] != cmd {
						done <- fmt.Errorf("got %x, want command %x", request, cmd)
						return
					}
					response := make([]byte, 32)
					response[0] = 0xa8
					response[2] = 29
					if _, err := b.Write(response); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			c := &Client{conn: a, ctx: context.Background(), timeout: time.Second, reserved: true, pageComplete: complete}
			c.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCancelInterruptsBlockedRead(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c := &Client{conn: a, ctx: ctx, timeout: time.Minute}
	c.stop = context.AfterFunc(ctx, func() { a.Close() })
	defer c.Close()
	ready := make(chan struct{})
	go func() { buf := make([]byte, 4); _, _ = io.ReadFull(b, buf); close(ready) }()
	done := make(chan error, 1)
	go func() { _, err := c.Inquiry(); done <- err }()
	<-ready
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not interrupt read")
	}
}

func TestFeederKeepsPrefetchedPagesOnOneConnection(t *testing.T) {
	exchanges := fixture(t)
	responses := make(map[byte][]byte)
	var reads, images [][]byte
	for _, e := range exchanges {
		cmd := e.Command[2]
		if responses[cmd] == nil {
			responses[cmd] = e.Response
		}
		if cmd == 0x28 && e.Response[1] == 0 {
			reads = append(reads, e.Response)
		}
		if cmd == 0x29 {
			images = append(images, e.Response)
		}
	}
	a, b := net.Pipe()
	defer b.Close()
	counts := make(map[byte]int)
	done := make(chan error, 1)
	go func() {
		defer b.Close()
		ri, ii := 0, 0
		for {
			var header [4]byte
			if _, err := io.ReadFull(b, header[:]); err != nil {
				done <- err
				return
			}
			cmd := header[2]
			counts[cmd]++
			response := responses[cmd]
			switch cmd {
			case 0x31:
				if counts[cmd] == 3 {
					response = make([]byte, 32)
					copy(response, []byte{0xa8, 2, 29, 0})
					response[5] = 0x10
				}
			case 0x24:
				rest := make([]byte, 21)
				if _, err := io.ReadFull(b, rest); err != nil {
					done <- err
					return
				}
				if rest[19] != 0x20 {
					done <- fmt.Errorf("not a feeder request: %x", rest)
					return
				}
			case 0x28:
				if ri < 2*len(reads) {
					response = reads[ri%len(reads)]
				} else {
					response = make([]byte, 32)
					copy(response, []byte{0xa8, 2, 29, 0x20})
					response[13] = 0x10
				}
				ri++
			case 0x29:
				response = images[ii%len(images)]
				ii++
			}
			if response == nil {
				done <- fmt.Errorf("unknown command %x", cmd)
				return
			}
			if _, err := b.Write(response); err != nil {
				done <- err
				return
			}
			if cmd == 0x17 {
				done <- nil
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := &Client{conn: a, ctx: ctx, timeout: time.Second}
	defer c.Close()
	s := Settings{DPI: 75, Width: 2552, Height: 4200, Source: "Feeder", Format: "image/png"}
	for page := 0; page < 2; page++ {
		img, err := c.Scan(s)
		if err != nil {
			t.Fatalf("page %d: %v", page+1, err)
		}
		if img.Bounds() != image.Rect(0, 0, 638, 880) {
			t.Fatal(img.Bounds())
		}
	}
	if _, err := c.Scan(s); !errors.Is(err, ErrNoPaper) {
		t.Fatalf("batch end: %v", err)
	}
	c.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []byte{0x12, 0x16, 0x24, 0x31, 0x17} {
		want := 1
		if cmd == 0x31 {
			want = 3
		}
		if counts[cmd] != want {
			t.Errorf("command %x sent %d times during batch", cmd, counts[cmd])
		}
	}
}

func TestReplayRealTwoPageFeederCapture(t *testing.T) {
	exchanges := privateCapture(t, "xerox-feeder-2pages.pcap")
	if len(exchanges) != 471 {
		t.Fatalf("unexpected capture: %d commands", len(exchanges))
	}
	a, b := net.Pipe()
	defer b.Close()
	done := make(chan error, 1)
	go func() {
		defer b.Close()
		for index, e := range exchanges {
			request := make([]byte, len(e.Command))
			if _, err := io.ReadFull(b, request); err != nil {
				done <- fmt.Errorf("command %d: %w", index, err)
				return
			}
			want := append([]byte(nil), e.Command...)
			if want[2] == 0x24 {
				// The bridge crops in software; the vendor uses a 0.12 inch
				// hardware X offset, which is immaterial to framing this replay.
				want[16] = 0
			}
			if !bytes.Equal(request, want) {
				done <- fmt.Errorf("command %d: got %x want %x", index, request, want)
				return
			}
			response := e.Response
			for len(response) > 0 {
				n := min(127, len(response))
				if _, err := b.Write(response[:n]); err != nil {
					done <- err
					return
				}
				response = response[n:]
			}
		}
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c := &Client{conn: a, ctx: ctx, timeout: 3 * time.Second}
	defer c.Close()
	settings := Settings{DPI: 300, Width: 2480, Height: 3508, Source: "Feeder", Format: "image/png"}
	for page := 1; page <= 2; page++ {
		img, err := c.Scan(settings)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if img.Bounds() != image.Rect(0, 0, 2480, 3488) {
			t.Fatalf("page %d bounds: %v", page, img.Bounds())
		}
	}
	if _, err := c.Scan(settings); !errors.Is(err, ErrNoPaper) {
		t.Fatalf("end of batch: %v", err)
	}
	c.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReplayRealFlatbedCapture(t *testing.T) {
	exchanges := privateCapture(t, "xerox-overview-scan.pcap")
	img, _, bands, err := CaptureImage(exchanges)
	if err != nil {
		t.Fatal(err)
	}
	if len(exchanges) != 79 || bands != 28 || img.Bounds() != image.Rect(0, 0, 638, 880) {
		t.Fatalf("unexpected flatbed capture: %d commands, %d bands, %v", len(exchanges), bands, img.Bounds())
	}
}
