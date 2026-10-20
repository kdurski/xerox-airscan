package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"xerox-airscan/internal/config"
)

type options struct {
	command      string
	config       config.Config
	dpi          int
	source       string
	gray         bool
	output       string
	capture      string
	retryStartup bool
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `Usage: xerox-airscan COMMAND [options]

Commands:
  serve        Advertise the scanner to Apple Image Capture
  doctor       Query scanner identity and capabilities
  scan         Scan a flatbed page or an entire feeder batch to image files
  decode-pcap  Reconstruct an image from a local protocol capture

Configuration: config.json in the current directory; --config selects a file.
Use COMMAND --help for command-specific options.`)
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var o options
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		usage(stderr)
		return o, flag.ErrHelp
	}
	o.command = args[0]
	if o.command != "serve" && o.command != "doctor" && o.command != "scan" && o.command != "decode-pcap" {
		return o, fmt.Errorf("unknown command %q", o.command)
	}
	f := flag.NewFlagSet(o.command, flag.ContinueOnError)
	f.SetOutput(stderr)
	var path, printer, listen, name string
	var bonjour, feeder, verbose bool
	if o.command != "decode-pcap" {
		f.StringVar(&path, "config", config.DefaultPath, "configuration file")
		f.StringVar(&printer, "printer", "", "override scanner_address: HOST[:PORT]")
	}
	if o.command == "serve" {
		defaults := config.Defaults()
		f.StringVar(&listen, "listen", defaults.ListenAddress, "override listen_address")
		f.StringVar(&name, "name", defaults.Name, "override Bonjour scanner name")
		f.BoolVar(&bonjour, "bonjour", true, "override Bonjour publishing")
		f.BoolVar(&feeder, "adf", true, "override feeder advertising")
		f.BoolVar(&verbose, "verbose", false, "override protocol diagnostics")
		f.BoolVar(&o.retryStartup, "retry-startup", false, "wait and retry every 30 seconds until the scanner is reachable")
	}
	if o.command == "scan" {
		f.IntVar(&o.dpi, "dpi", 300, "scan resolution")
		f.StringVar(&o.source, "source", "Platen", "Platen or Feeder")
		f.BoolVar(&o.gray, "gray", false, "produce grayscale")
	}
	if o.command == "scan" || o.command == "decode-pcap" {
		f.StringVar(&o.output, "output", "scan.png", "output .png or .jpg file")
	}
	if o.command == "decode-pcap" {
		f.StringVar(&o.capture, "capture", "", "input classic Ethernet pcap (required)")
	}
	if err := f.Parse(args[1:]); err != nil {
		return o, err
	}
	if f.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if o.command == "decode-pcap" {
		if o.capture == "" {
			return o, errors.New("--capture is required")
		}
		return o, nil
	}
	visited := make(map[string]bool)
	f.Visit(func(v *flag.Flag) { visited[v.Name] = true })
	cfg, err := config.Load(path, visited["config"])
	if err != nil {
		return o, err
	}
	if visited["printer"] {
		cfg.ScannerAddress = printer
	}
	if visited["listen"] {
		cfg.ListenAddress = listen
	}
	if visited["name"] {
		cfg.Name = name
	}
	if visited["bonjour"] {
		cfg.Bonjour = bonjour
	}
	if visited["adf"] {
		cfg.Feeder = feeder
	}
	if visited["verbose"] {
		cfg.Verbose = verbose
	}
	if err := cfg.Validate(); err != nil {
		return o, err
	}
	o.config = cfg
	return o, nil
}
