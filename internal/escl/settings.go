package escl

import (
	"encoding/xml"
	"errors"
	"io"
	"strings"
	"xerox-airscan/internal/xerox"
)

type scanSettings struct {
	XMLName   xml.Name
	Source    string `xml:"InputSource"`
	Color     string `xml:"ColorMode"`
	XRes      int    `xml:"XResolution"`
	YRes      int    `xml:"YResolution"`
	Format    string `xml:"DocumentFormat"`
	FormatExt string `xml:"DocumentFormatExt"`
	Duplex    bool   `xml:"Duplex"`
	Regions   []struct {
		Units  string `xml:"ContentRegionUnits"`
		X      int    `xml:"XOffset"`
		Y      int    `xml:"YOffset"`
		Width  int    `xml:"Width"`
		Height int    `xml:"Height"`
	} `xml:"ScanRegions>ScanRegion"`
}

func ParseSettings(body io.Reader, info xerox.Info, adf bool) (xerox.Settings, error) {
	var raw scanSettings
	d := xml.NewDecoder(body)
	if err := d.Decode(&raw); err != nil {
		return xerox.Settings{}, err
	}
	// Reject extra XML documents or text after the request.
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return xerox.Settings{}, err
		}
		if text, ok := tok.(xml.CharData); !ok || strings.TrimSpace(string(text)) != "" {
			return xerox.Settings{}, errors.New("extra content after ScanSettings")
		}
	}
	if raw.XMLName.Local != "ScanSettings" {
		return xerox.Settings{}, errors.New("expected ScanSettings")
	}
	if raw.Duplex {
		return xerox.Settings{}, errors.New("duplex scanning is not supported")
	}
	if raw.Source == "" {
		raw.Source = "Platen"
	}
	if raw.Source == "Feeder" && !adf {
		return xerox.Settings{}, errors.New("feeder support requires --adf")
	}
	if raw.Color == "" {
		raw.Color = "RGB24"
	}
	if raw.Color != "RGB24" && raw.Color != "Grayscale8" {
		return xerox.Settings{}, errors.New("unsupported ColorMode")
	}
	if raw.XRes == 0 {
		raw.XRes = 300
	}
	if raw.YRes == 0 {
		raw.YRes = raw.XRes
	}
	if raw.XRes != raw.YRes {
		return xerox.Settings{}, errors.New("XResolution and YResolution must match")
	}
	format := raw.FormatExt
	if format == "" {
		format = raw.Format
	}
	if format == "" {
		format = "image/jpeg"
	}
	h := info.PlatenHeight / 4
	if raw.Source == "Feeder" {
		h = info.ADFHeight / 4
	}
	settings := xerox.Settings{DPI: raw.XRes, Width: info.Width / 4, Height: h, Source: raw.Source, Gray: raw.Color == "Grayscale8", Format: format}
	if len(raw.Regions) > 1 {
		return settings, errors.New("only one scan region is supported")
	}
	if len(raw.Regions) == 1 {
		r := raw.Regions[0]
		if r.Units != "" && r.Units != "escl:ThreeHundredthsOfInches" && r.Units != "ThreeHundredthsOfInches" {
			return settings, errors.New("unsupported ContentRegionUnits")
		}
		settings.X, settings.Y, settings.Width, settings.Height = r.X, r.Y, r.Width, r.Height
	}
	return settings, settings.Validate(info)
}
