package escl

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLocked()
	if s.ADF && s.active == nil && time.Since(s.statusChecked) >= 2*time.Second {
		// Serialize the short, unreserved inquiry with job creation: the
		// printer only supports one port-9400 conversation at a time.
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		info, err := s.probe(ctx)
		cancel()
		s.statusChecked = time.Now()
		if err == nil {
			s.adfState = "ScannerAdfEmpty"
			if info.PaperLoaded {
				s.adfState = "ScannerAdfLoaded"
			}
		} else {
			s.adfState = ""
		}
	}
	state := "Idle"
	if s.active != nil {
		state = "Processing"
	}
	var jobs strings.Builder
	for _, j := range s.jobs {
		jobs.WriteString(jobXML(j))
	}
	adf := ""
	if s.ADF && s.adfState != "" {
		adf = "<scan:AdfState>" + s.adfState + "</scan:AdfState>"
	}
	xmlReply(w, fmt.Sprintf(`<scan:ScannerStatus %s><pwg:Version>2.0</pwg:Version><pwg:State>%s</pwg:State>%s<scan:Jobs>%s</scan:Jobs></scan:ScannerStatus>`, namespaces, state, adf, jobs.String()))
}
