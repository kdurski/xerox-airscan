package escl

import "encoding/xml"

const (
	pwgNamespace  = "http://www.pwg.org/schemas/2010/12/sm"
	scanNamespace = "http://schemas.hp.com/imaging/escl/2011/05/03"
)

type resolution struct {
	X int `xml:"scan:XResolution"`
	Y int `xml:"scan:YResolution"`
}

type settingProfile struct {
	Name        string       `xml:"name,attr"`
	ColorModes  []string     `xml:"scan:ColorModes>scan:ColorMode"`
	ColorSpaces []string     `xml:"scan:ColorSpaces>scan:ColorSpace"`
	Formats     []string     `xml:"scan:DocumentFormats>pwg:DocumentFormat"`
	Resolutions []resolution `xml:"scan:SupportedResolutions>scan:DiscreteResolutions>scan:DiscreteResolution"`
}

type inputCapabilities struct {
	MinWidth       int            `xml:"scan:MinWidth"`
	MinHeight      int            `xml:"scan:MinHeight"`
	MaxWidth       int            `xml:"scan:MaxWidth"`
	MaxHeight      int            `xml:"scan:MaxHeight"`
	PhysicalWidth  int            `xml:"scan:MaxPhysicalWidth"`
	PhysicalHeight int            `xml:"scan:MaxPhysicalHeight"`
	MaxRegions     int            `xml:"scan:MaxScanRegions"`
	Profile        settingProfile `xml:"scan:SettingProfiles>scan:SettingProfile"`
	Intents        []string       `xml:"scan:SupportedIntents>scan:SupportedIntent"`
}

type feederCapabilities struct {
	Simplex inputCapabilities `xml:"scan:AdfSimplexInputCaps"`
	Options struct{}          `xml:"scan:AdfOptions"`
}

type capabilitiesDocument struct {
	XMLName       xml.Name            `xml:"scan:ScannerCapabilities"`
	PWGNamespace  string              `xml:"xmlns:pwg,attr"`
	ScanNamespace string              `xml:"xmlns:scan,attr"`
	Version       string              `xml:"pwg:Version"`
	Model         string              `xml:"pwg:MakeAndModel"`
	Serial        string              `xml:"pwg:SerialNumber"`
	UUID          string              `xml:"scan:UUID"`
	Platen        inputCapabilities   `xml:"scan:Platen>scan:PlatenInputCaps"`
	Feeder        *feederCapabilities `xml:"scan:Adf,omitempty"`
}

func (s *Server) inputCapabilities(height int, preview bool) inputCapabilities {
	profile := settingProfile{Name: "default", ColorModes: []string{"RGB24", "Grayscale8"}, ColorSpaces: []string{"RGB"}, Formats: []string{"image/jpeg", "image/png"}}
	for _, dpi := range s.Info.Resolutions {
		profile.Resolutions = append(profile.Resolutions, resolution{X: dpi, Y: dpi})
	}
	intents := []string{"Document", "Photo"}
	if preview {
		intents = append(intents, "Preview")
	}
	return inputCapabilities{MinWidth: 4, MinHeight: 4, MaxWidth: s.Info.Width / 4, MaxHeight: height / 4, PhysicalWidth: s.Info.Width / 4, PhysicalHeight: height / 4, MaxRegions: 1, Profile: profile, Intents: intents}
}

func (s *Server) capabilities() (string, error) {
	id := UUID(s.Address)
	document := capabilitiesDocument{PWGNamespace: pwgNamespace, ScanNamespace: scanNamespace, Version: "2.0", Model: s.Info.Model, Serial: id, UUID: id, Platen: s.inputCapabilities(s.Info.PlatenHeight, true)}
	if s.ADF {
		document.Feeder = &feederCapabilities{Simplex: s.inputCapabilities(s.Info.ADFHeight, false)}
	}
	data, err := xml.Marshal(document)
	return string(data), err
}
