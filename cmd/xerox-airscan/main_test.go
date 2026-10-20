package main

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

type retryNotice chan struct{}

func (w retryNotice) Write(p []byte) (int, error) {
	select {
	case w <- struct{}{}:
	default:
	}
	return len(p), nil
}

func TestStartupRetryCanBeStoppedWhilePrinterUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if conn, err := ln.Accept(); err == nil {
			// Simulate a printer that drops the startup inquiry.
			conn.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notice := make(retryNotice, 1)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve", "--printer", ln.Addr().String(), "--retry-startup"}, io.Discard, notice)
	}()
	select {
	case <-notice:
	case err := <-done:
		t.Fatalf("exited instead of waiting for the printer: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("did not reach startup retry")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for the 30-second retry interval")
	}
}
