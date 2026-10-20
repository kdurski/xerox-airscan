package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"time"

	"xerox-airscan/internal/config"
	"xerox-airscan/internal/escl"
	"xerox-airscan/internal/xerox"
)

func serve(ctx context.Context, cfg config.Config, info xerox.Info, stdout, stderr io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ln, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return err
	}
	defer ln.Close()
	bridge := escl.New(cfg.ScannerAddress, info, cfg.Feeder)
	bridge.Debug = cfg.Verbose
	defer bridge.Close()
	server := &http.Server{Handler: bridge, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 6 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	publisherErr := make(chan error, 1)
	if cfg.Bonjour {
		_, port, err := net.SplitHostPort(ln.Addr().String())
		if err != nil {
			return err
		}
		cmd := bonjourCommand(ctx, cfg, info, port)
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("Bonjour registration: %w (use --bonjour=false for manual HTTP testing)", err)
		}
		go func() {
			err := cmd.Wait()
			if ctx.Err() == nil {
				if err == nil {
					err = errors.New("publisher exited unexpectedly")
				}
				publisherErr <- err
				cancel()
			}
		}()
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		bridge.Close()
		shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		_ = server.Shutdown(shutdownCtx)
	}()
	log.Printf("Serving %s on %s; select %q in Image Capture", info.Model, ln.Addr(), cfg.Name)
	err = server.Serve(ln)
	cancel()
	<-shutdownDone
	select {
	case publisherFailure := <-publisherErr:
		return fmt.Errorf("Bonjour publisher stopped: %w", publisherFailure)
	default:
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func bonjourCommand(ctx context.Context, cfg config.Config, info xerox.Info, port string) *exec.Cmd {
	sources := "platen"
	if cfg.Feeder {
		sources += ",adf"
	}
	return exec.CommandContext(ctx, "/usr/bin/dns-sd", "-R", cfg.Name, "_uscan._tcp", "local.", port,
		"txtvers=1", "vers=2.0", "rs=eSCL", "ty="+info.Model, "pdl=image/jpeg,image/png",
		"cs=color,grayscale", "is="+sources, "duplex=F", "UUID="+escl.UUID(cfg.ScannerAddress))
}
