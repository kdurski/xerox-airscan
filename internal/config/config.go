// Package config loads local bridge settings. Scanner addresses have no default.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"unicode"
)

const DefaultPath = "config.json"

type Config struct {
	ScannerAddress string `json:"scanner_address"`
	ListenAddress  string `json:"listen_address"`
	Name           string `json:"name"`
	Bonjour        bool   `json:"bonjour"`
	Feeder         bool   `json:"feeder"`
	Verbose        bool   `json:"verbose"`
}

func Defaults() Config {
	return Config{ListenAddress: "0.0.0.0:8090", Name: "Xerox WorkCentre 3225 AirScan", Bonjour: true, Feeder: true}
}

// Load applies a JSON file over defaults. Only an absent implicit default file
// is optional; an explicitly selected path, malformed JSON, or unknown key fails.
func Load(path string, required bool) (Config, error) {
	cfg := Defaults()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !required {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("load config %s: %w", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if len(data) > 64<<10 {
		return cfg, fmt.Errorf("config %s exceeds 64 KiB", path)
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	target := &cfg
	if err := d.Decode(&target); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	if target == nil {
		return cfg, fmt.Errorf("config %s must be a JSON object", path)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return cfg, fmt.Errorf("config %s must contain exactly one JSON object", path)
	}
	return cfg, nil
}

// Validate runs after CLI overrides, so --printer can replace a missing address.
func (c *Config) Validate() error {
	address, err := ScannerAddress(c.ScannerAddress)
	if err != nil {
		return err
	}
	c.ScannerAddress = address
	if err := validateEndpoint(c.ListenAddress, true); err != nil {
		return fmt.Errorf("listen_address: %w", err)
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Name) > 63 || strings.IndexFunc(c.Name, unicode.IsControl) >= 0 {
		return errors.New("name must contain 1–63 bytes without control characters")
	}
	return nil
}

func ScannerAddress(address string) (string, error) {
	if address == "" {
		return "", errors.New("set scanner_address in config.json (see config.example.json), or use --printer HOST[:PORT]")
	}
	if net.ParseIP(strings.Trim(address, "[]")) != nil {
		address = net.JoinHostPort(strings.Trim(address, "[]"), "9400")
	} else if !strings.Contains(address, ":") {
		address = net.JoinHostPort(address, "9400")
	}
	if err := validateEndpoint(address, false); err != nil {
		return "", fmt.Errorf("scanner_address: %w", err)
	}
	return address, nil
}

func validateEndpoint(address string, listen bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return errors.New("expected a host/IP and numeric port, without a URL scheme")
	}
	if (!listen && host == "") || strings.ContainsAny(host, " /\\\t\r\n@?#\x00") {
		return errors.New("invalid hostname or IP address")
	}
	n, err := strconv.Atoi(port)
	minimum := 1
	if listen {
		minimum = 0
	}
	if err != nil || n < minimum || n > 65535 {
		return errors.New("port is outside the allowed range")
	}
	return nil
}
