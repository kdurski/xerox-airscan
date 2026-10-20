// Package escl serves the AirScan/eSCL endpoints used by Apple Image Capture.
package escl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"xerox-airscan/internal/xerox"
)

const namespaces = `xmlns:pwg="http://www.pwg.org/schemas/2010/12/sm" xmlns:scan="http://schemas.hp.com/imaging/escl/2011/05/03"`

type job struct {
	id        string
	settings  xerox.Settings
	ctx       context.Context
	cancel    context.CancelFunc
	state     string
	reason    string
	pages     int
	inFlight  bool
	created   time.Time
	idleTimer *time.Timer
	client    *xerox.Client
}

type Server struct {
	Address       string
	Info          xerox.Info
	ADF           bool
	Timeout       time.Duration
	Debug         bool
	mu            sync.Mutex
	jobs          map[string]*job
	active        *job
	adfState      string
	statusChecked time.Time
	probe         func(context.Context) (xerox.Info, error)
	scanPage      func(context.Context, *job) (image.Image, error)
}

func New(address string, info xerox.Info, adf bool) *Server {
	s := &Server{Address: address, Info: info, ADF: adf, Timeout: 5 * time.Minute, jobs: make(map[string]*job), adfState: "ScannerAdfEmpty"}
	if info.PaperLoaded {
		s.adfState = "ScannerAdfLoaded"
	}
	s.probe = func(ctx context.Context) (xerox.Info, error) {
		client, err := xerox.Dial(ctx, address, 2*time.Second)
		if err != nil {
			return xerox.Info{}, err
		}
		defer client.Close()
		return client.Inquiry()
	}
	s.scanPage = func(ctx context.Context, job *job) (image.Image, error) {
		if job.client == nil {
			client, err := xerox.Dial(job.ctx, address, 20*time.Second)
			if err != nil {
				return nil, err
			}
			client.Debug = s.Debug
			job.client = client
		}
		// One connection and reservation own the entire feeder batch. The
		// firmware can prefetch sheets whose pixels still need downloading.
		return job.client.Scan(job.settings)
	}
	return s
}

func UUID(address string) string {
	sum := sha256.Sum256([]byte("xerox-airscan:" + address))
	b := sum[:16]
	b[6] = (b[6] & 15) | 0x50
	b[8] = (b[8] & 63) | 0x80
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func xmlReply(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	_, _ = io.WriteString(w, xml.Header+body)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.Debug {
		log.Printf("eSCL %s %s", r.Method, r.URL.Path)
	}
	w.Header().Set("Cache-Control", "no-store")
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case path == "" && r.Method == "GET":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintf(w, "%s AirScan bridge\nCapabilities: /eSCL/ScannerCapabilities\nStatus: /eSCL/ScannerStatus\n", s.Info.Model)
	case path == "/eSCL/ScannerCapabilities" && r.Method == "GET":
		body, err := s.capabilities()
		if err != nil {
			http.Error(w, "cannot encode scanner capabilities", http.StatusInternalServerError)
			return
		}
		xmlReply(w, body)
	case path == "/eSCL/ScannerStatus" && r.Method == "GET":
		s.status(w, r)
	case path == "/eSCL/ScanJobs" && r.Method == "POST":
		s.create(w, r)
	case strings.HasPrefix(path, "/eSCL/ScanJobs/"):
		parts := strings.Split(strings.TrimPrefix(path, "/eSCL/ScanJobs/"), "/")
		if len(parts) == 2 && parts[1] == "NextDocument" && r.Method == "GET" {
			s.next(w, r, parts[0])
			return
		}
		if len(parts) == 1 && r.Method == "DELETE" {
			s.cancel(w, parts[0])
			return
		}
		if len(parts) == 1 && r.Method == "GET" {
			s.jobInfo(w, parts[0])
			return
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		j.cancel()
	}
}
