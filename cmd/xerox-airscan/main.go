package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"xerox-airscan/internal/xerox"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	o, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	if o.command == "decode-pcap" {
		return decodeCapture(o, stdout)
	}
	info, err := queryScanner(ctx, o.config.ScannerAddress)
	for err != nil && o.retryStartup && ctx.Err() == nil {
		fmt.Fprintf(stderr, "Scanner unavailable: %v; retrying in 30 seconds\n", err)
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		info, err = queryScanner(ctx, o.config.ScannerAddress)
	}
	if err != nil {
		return err
	}
	switch o.command {
	case "doctor":
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(info)
	case "scan":
		return scan(ctx, o, info, stdout)
	case "serve":
		return serve(ctx, o.config, info, stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", o.command)
	}
}

func queryScanner(ctx context.Context, address string) (xerox.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	client, err := xerox.Dial(ctx, address, 20*time.Second)
	if err != nil {
		return xerox.Info{}, err
	}
	defer client.Close()
	return client.Inquiry()
}

func decodeCapture(o options, stdout io.Writer) error {
	exchanges, err := xerox.ReadCapture(o.capture)
	if err != nil {
		return err
	}
	img, info, bands, err := xerox.CaptureImage(exchanges)
	if err != nil {
		return err
	}
	if err := writeImage(o.output, img); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: %d commands, %d JPEG strips, %dx%d pixels → %s\n", info.Model, len(exchanges), bands, img.Bounds().Dx(), img.Bounds().Dy(), o.output)
	return nil
}
