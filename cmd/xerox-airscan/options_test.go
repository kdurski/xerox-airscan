package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIOverridesConfigIncludingFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"scanner_address":"scanner.local","bonjour":true,"feeder":true,"verbose":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := parseOptions([]string{"serve", "--config", path, "--printer", "192.0.2.20", "--bonjour=false", "--adf=false", "--verbose=false"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.config.ScannerAddress != "192.0.2.20:9400" || o.config.Bonjour || o.config.Feeder || o.config.Verbose {
		t.Fatalf("%+v", o.config)
	}
	o, err = parseOptions([]string{"doctor", "--config", path}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.config.ScannerAddress != "scanner.local:9400" || !o.config.Bonjour {
		t.Fatalf("config not used: %+v", o.config)
	}
}

func TestHelpAndOfflineCommandNeedNoConfig(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"serve", "--help"}} {
		if _, err := parseOptions(args, io.Discard); !errors.Is(err, flag.ErrHelp) {
			t.Fatalf("%v: %v", args, err)
		}
	}
	if _, err := parseOptions([]string{"decode-pcap", "--capture", "example.pcap"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	if _, err := parseOptions([]string{"decode-pcap"}, io.Discard); err == nil {
		t.Fatal("missing capture accepted")
	}
	if _, err := parseOptions([]string{"serve", "--config", filepath.Join(t.TempDir(), "missing.json"), "--printer", "scanner.local"}, io.Discard); err == nil {
		t.Fatal("explicit missing config ignored")
	}
}

func TestPrinterOverrideWithoutLocalConfig(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := parseOptions([]string{"doctor"}, io.Discard); err == nil {
		t.Fatal("missing scanner configuration accepted")
	}
	o, err := parseOptions([]string{"doctor", "--printer", "scanner.local"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.config.ScannerAddress != "scanner.local:9400" {
		t.Fatal(o.config.ScannerAddress)
	}
}
