package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPreservesDefaultsAndExplicitFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"scanner_address":"scanner.local","bonjour":false,"feeder":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.ScannerAddress != "scanner.local:9400" || cfg.Bonjour || cfg.Feeder || cfg.ListenAddress != "0.0.0.0:8090" {
		t.Fatalf("%+v", cfg)
	}
}

func TestRejectBadConfiguration(t *testing.T) {
	for _, body := range []string{`null`, `{"scanner_addres":"scanner.local"}`, `{"bonjour":"false"}`, `{} {}`, `{"scanner_address":`, `{}` + strings.Repeat(" ", 64<<10)} {
		path := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, true); err == nil {
			t.Fatalf("accepted %q", body)
		}
	}
	path := filepath.Join(t.TempDir(), "missing.json")
	if _, err := Load(path, false); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, true); err == nil {
		t.Fatal("explicit missing config accepted")
	}
	cfg := Defaults()
	if err := cfg.Validate(); err == nil {
		t.Fatal("hard-coded scanner default unexpectedly exists")
	}
}

func TestInvalidServerSettings(t *testing.T) {
	for _, listen := range []string{"localhost", ":bad", ":65536"} {
		cfg := Defaults()
		cfg.ScannerAddress = "scanner.local"
		cfg.ListenAddress = listen
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted listen address %q", listen)
		}
	}
	for _, name := range []string{"", strings.Repeat("x", 64), "name\twith tab"} {
		cfg := Defaults()
		cfg.ScannerAddress = "scanner.local"
		cfg.Name = name
		if err := cfg.Validate(); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}
}

func TestScannerAddresses(t *testing.T) {
	for input, want := range map[string]string{"192.0.2.10": "192.0.2.10:9400", "scanner.local:9500": "scanner.local:9500", "2001:db8::1": "[2001:db8::1]:9400", "[2001:db8::1]:9400": "[2001:db8::1]:9400"} {
		got, err := ScannerAddress(input)
		if err != nil || got != want {
			t.Fatalf("%s → %s, %v", input, got, err)
		}
	}
	for _, input := range []string{"", "http://scanner.local", ":9400", "scanner.local:0", "scanner.local:65536", "scanner.local:scan", "bad host"} {
		if _, err := ScannerAddress(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
