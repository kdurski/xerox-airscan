package escl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"log"
	"net/http"
	"time"
	"xerox-airscan/internal/xerox"
)

func jobXML(j *job) string {
	return fmt.Sprintf(`<scan:JobInfo><pwg:JobUri>/eSCL/ScanJobs/%s</pwg:JobUri><pwg:JobUuid>%s</pwg:JobUuid><scan:Age>%d</scan:Age><pwg:JobState>%s</pwg:JobState><pwg:ImagesCompleted>%d</pwg:ImagesCompleted><pwg:JobStateReasons><pwg:JobStateReason>%s</pwg:JobStateReason></pwg:JobStateReasons></scan:JobInfo>`, j.id, j.id, int(time.Since(j.created).Seconds()), j.state, j.pages, j.reason)
}

func (s *Server) expireLocked() {
	for id, j := range s.jobs {
		if j.ctx.Err() != nil && !j.inFlight && (j.state == "Pending" || j.state == "Processing") {
			j.state = "Canceled"
			j.reason = "JobCanceledByUser"
			if s.active == j {
				s.active = nil
			}
		}
		if !j.inFlight && s.active != j && time.Since(j.created) > 10*time.Minute {
			j.cancel()
			delete(s.jobs, id)
		}
	}
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	settings, err := ParseSettings(http.MaxBytesReader(w, r.Body, 64<<10), s.Info, s.ADF)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if s.Debug {
		log.Printf("scan settings: %+v", settings)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.active != nil {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "scanner is busy", 503)
		return
	}
	if len(s.jobs) >= 32 {
		var oldest *job
		for _, j := range s.jobs {
			if oldest == nil || j.created.Before(oldest.created) {
				oldest = j
			}
		}
		oldest.cancel()
		delete(s.jobs, oldest.id)
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		http.Error(w, "cannot allocate job", 500)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(s.Timeout, cancel)
	j := &job{id: hex.EncodeToString(idBytes), settings: settings, ctx: ctx, cancel: func() { timer.Stop(); cancel() }, idleTimer: timer, state: "Pending", reason: "JobQueued", created: time.Now()}
	s.jobs[j.id] = j
	s.active = j
	w.Header().Set("Location", "http://"+r.Host+"/eSCL/ScanJobs/"+j.id)
	w.WriteHeader(http.StatusCreated)
}

func (s *Server) jobInfo(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	j := s.jobs[id]
	if j == nil {
		http.Error(w, "unknown job", 404)
		return
	}
	xmlReply(w, `<scan:ScannerStatus `+namespaces+`><scan:Jobs>`+jobXML(j)+`</scan:Jobs></scan:ScannerStatus>`)
}

func (s *Server) cancel(w http.ResponseWriter, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.jobs[id]
	if j == nil {
		http.Error(w, "unknown job", 404)
		return
	}
	j.cancel()
	j.state = "Canceled"
	j.reason = "JobCanceledByUser"
	if !j.inFlight && s.active == j {
		s.active = nil
	}
	w.WriteHeader(200)
}

func (s *Server) next(w http.ResponseWriter, r *http.Request, id string) {
	s.mu.Lock()
	s.expireLocked()
	j := s.jobs[id]
	if j == nil {
		s.mu.Unlock()
		http.Error(w, "unknown job", 404)
		return
	}
	if j.inFlight {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		http.Error(w, "page is being acquired", 503)
		return
	}
	if j.state != "Pending" {
		s.mu.Unlock()
		http.Error(w, "job is finished", 410)
		return
	}
	j.inFlight = true
	j.idleTimer.Reset(s.Timeout)
	j.state = "Processing"
	j.reason = "JobScanning"
	s.mu.Unlock()
	// A disconnected Image Capture request must interrupt acquisition too.
	ctx, cancel := context.WithCancel(j.ctx)
	defer cancel()
	stop := context.AfterFunc(r.Context(), func() { cancel(); j.cancel() })
	defer stop()
	var output bytes.Buffer
	err := func() error {
		img, err := s.scanPage(ctx, j)
		if err != nil {
			return err
		}
		if j.settings.Format == "image/png" {
			return png.Encode(&output, img)
		}
		return jpeg.Encode(&output, img, &jpeg.Options{Quality: 95})
	}()
	if j.client != nil && (err != nil || j.settings.Source == "Platen" || j.ctx.Err() != nil) {
		_ = j.client.Close()
		j.client = nil
	}
	s.mu.Lock()
	j.inFlight = false
	if err != nil {
		noPaper := errors.Is(err, xerox.ErrNoPaper) && j.settings.Source == "Feeder"
		j.state = "Aborted"
		j.reason = "ErrorsDetected"
		if noPaper {
			s.adfState = "ScannerAdfEmpty"
			if j.pages > 0 {
				j.state = "Completed"
				j.reason = "JobCompletedSuccessfully"
			} else {
				j.reason = "ResourcesAreNotReady"
			}
		}
		if errors.Is(err, xerox.ErrPaperJam) {
			s.adfState = "ScannerAdfJam"
		}
		if errors.Is(err, xerox.ErrCoverOpen) {
			s.adfState = "ScannerAdfDoorOpen"
		}
		s.statusChecked = time.Time{}
		if ctx.Err() != nil {
			j.state = "Canceled"
			j.reason = "JobCanceledByUser"
		}
		if s.active == j {
			s.active = nil
		}
		j.cancel()
		s.mu.Unlock()
		if noPaper {
			http.Error(w, "document feeder is empty", 410)
			return
		}
		log.Printf("scan failed: %v", err)
		http.Error(w, "scan failed: "+err.Error(), 502)
		return
	}
	if j.ctx.Err() != nil {
		j.state = "Canceled"
		j.reason = "JobCanceledByUser"
		if s.active == j {
			s.active = nil
		}
		s.mu.Unlock()
		http.Error(w, "job canceled", 410)
		return
	}
	j.pages++
	if j.settings.Source == "Platen" {
		j.state = "Completed"
		j.reason = "JobCompletedSuccessfully"
		s.active = nil
		j.cancel()
	} else {
		j.state = "Pending"
		j.reason = "JobQueued"
		s.statusChecked = time.Time{}
		j.idleTimer.Reset(s.Timeout)
	}
	s.mu.Unlock()
	w.Header().Set("Content-Type", j.settings.Format)
	w.Header().Set("Content-Length", fmt.Sprint(output.Len()))
	_, _ = w.Write(output.Bytes())
}
