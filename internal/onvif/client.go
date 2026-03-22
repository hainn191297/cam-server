// Package onvif provides a thin wrapper around the ONVIF protocol for
// IP camera discovery and RTSP stream URI extraction.
//
// This is ported from the standalone onvif-adapter service and integrated
// directly into go-cam-server as an internal package, removing the need for
// a separate microservice.
package onvif

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	ritjonvif "github.com/ritj/onvif"
	"github.com/ritj/onvif/device"
	"github.com/ritj/onvif/ptz"
	"github.com/ritj/onvif/xsd"
	ritjxonvif "github.com/ritj/onvif/xsd/onvif"
	useonvif "github.com/use-go/onvif"
	"github.com/use-go/onvif/gosoap"
	"github.com/use-go/onvif/media"
	usexonvif "github.com/use-go/onvif/xsd/onvif"
)

// DiscoveredDevice holds the ONVIF endpoint (XAddr) for a discovered camera.
type DiscoveredDevice struct {
	Endpoint string `json:"endpoint"` // e.g. http://192.168.1.50:80/onvif/device_service
}

// StreamInfo holds the RTSP URIs and supported features extracted from an ONVIF device.
type StreamInfo struct {
	MainStream  string `json:"main_stream"`  // Highest resolution RTSP URL
	SubStream   string `json:"sub_stream"`   // Lower resolution RTSP URL (may be empty)
	CanPTZ      bool   `json:"can_ptz"`      // True if the main profile supports PTZ
	CanSnapshot bool   `json:"can_snapshot"` // True if the device supports snapshots
}

// Discover scans the given network interface for ONVIF devices.
// Use an empty or "all" nicName to probe all available interfaces via multicast.
func Discover(nicName string) ([]DiscoveredDevice, error) {
	if nicName == "" || nicName == "all" {
		// Try all interfaces: attempt each NIC registered on this host
		return discoverMulticast()
	}
	return discoverOnInterface(nicName)
}

// discoverOnInterface performs WS-Discovery on a specific NIC.
func discoverOnInterface(nicName string) ([]DiscoveredDevice, error) {
	found, err := useonvif.GetAvailableDevicesAtSpecificEthernetInterface(nicName)
	if err != nil {
		return nil, fmt.Errorf("discovery on %s: %w", nicName, err)
	}
	return toDevices(found), nil
}

// discoverMulticast attempts discovery on all interfaces by trying each one
// and aggregating results.
func discoverMulticast() ([]DiscoveredDevice, error) {
	nics, err := listNICNames()
	if err != nil {
		return nil, err
	}

	seen := map[string]bool{}
	var results []DiscoveredDevice

	for _, nic := range nics {
		found, err := useonvif.GetAvailableDevicesAtSpecificEthernetInterface(nic)
		if err != nil {
			// Ignore individual NIC errors (e.g., loopback or non-capable NICs).
			continue
		}
		for _, d := range found {
			ep := d.GetEndpoint("device")
			if !seen[ep] {
				seen[ep] = true
				results = append(results, DiscoveredDevice{Endpoint: ep})
			}
		}
	}
	return results, nil
}

func toDevices(raw []useonvif.Device) []DiscoveredDevice {
	out := make([]DiscoveredDevice, 0, len(raw))
	for _, d := range raw {
		out = append(out, DiscoveredDevice{Endpoint: d.GetEndpoint("device")})
	}
	return out
}

// GetStreams connects to a specific camera endpoint and returns its RTSP URIs.
// xaddr is the full ONVIF device URL, e.g. "http://192.168.1.50:80/onvif/device_service".
func GetStreams(xaddr, username, password string) (*StreamInfo, error) {
	u, err := url.Parse(xaddr)
	if err != nil {
		return nil, fmt.Errorf("invalid xaddr %q: %w", xaddr, err)
	}

	dev, err := ritjonvif.NewDevice(ritjonvif.DeviceParams{
		Xaddr:    fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		Username: username,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", xaddr, err)
	}

	// Sync time (best-effort, some cameras require it for digest auth)
	_, _ = dev.CallMethod(device.GetSystemDateAndTime{})

	// Fetch profiles
	profilesResp, err := dev.CallMethod(media.GetProfiles{})
	if err != nil {
		return nil, fmt.Errorf("GetProfiles: %w", err)
	}
	if profilesResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GetProfiles returned %d", profilesResp.StatusCode)
	}

	profilesSOAP := gosoap.SoapMessage(readBody(profilesResp))
	var mp mediaProfiles
	if err := xml.Unmarshal([]byte(profilesSOAP), &mp); err != nil {
		return nil, fmt.Errorf("parse profiles: %w", err)
	}
	profiles := mp.Body.GetProfilesResponse.Profiles
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no profiles found on device")
	}

	mainIdx, subIdx := classifyProfiles(profiles)
	mainProfile := profiles[mainIdx]

	info := &StreamInfo{
		CanPTZ: mainProfile.PTZConfiguration != nil,
	}

	// Check for snapshot capability (most cameras that support Media support it)
	_, snapErr := dev.CallMethod(media.GetSnapshotUri{ProfileToken: usexonvif.ReferenceToken(mainProfile.Token)})
	info.CanSnapshot = (snapErr == nil)

	for i, p := range profiles {
		if i != mainIdx && i != subIdx {
			continue
		}
		rtsp, err := fetchStreamURI(dev, p.Token)
		if err != nil {
			return nil, err
		}
		// Normalize hostname in URL to the device address (some cameras return internal IPs)
		rtsp = normalizeHost(rtsp, u.Hostname())
		if i == mainIdx {
			info.MainStream = rtsp
		} else {
			info.SubStream = rtsp
		}
	}
	return info, nil
}

// DeviceInfo holds manufacturer and hardware details from an ONVIF camera.
type DeviceInfo struct {
	Manufacturer    string `json:"manufacturer"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version"`
	SerialNumber    string `json:"serial_number"`
	HardwareID      string `json:"hardware_id"`
}

// GetDeviceInfo connects to an ONVIF device and returns its hardware info.
func GetDeviceInfo(xaddr, username, password string) (*DeviceInfo, error) {
	u, err := url.Parse(xaddr)
	if err != nil {
		return nil, fmt.Errorf("invalid xaddr %q: %w", xaddr, err)
	}

	dev, err := ritjonvif.NewDevice(ritjonvif.DeviceParams{
		Xaddr:    fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		Username: username,
		Password: password,
	})
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", xaddr, err)
	}

	resp, err := dev.CallMethod(device.GetDeviceInformation{})
	if err != nil {
		return nil, fmt.Errorf("GetDeviceInformation: %w", err)
	}
	if resp.StatusCode == 401 {
		return nil, fmt.Errorf("unauthorized: check username/password")
	}

	soap := gosoap.SoapMessage(readBody(resp))

	type deviInfo struct {
		XMLName xml.Name `xml:"Envelope"`
		Body    struct {
			Response struct {
				Manufacturer    string `xml:"Manufacturer"`
				Model           string `xml:"Model"`
				FirmwareVersion string `xml:"FirmwareVersion"`
				SerialNumber    string `xml:"SerialNumber"`
				HardwareId      string `xml:"HardwareId"`
			} `xml:"GetDeviceInformationResponse"`
		} `xml:"Body"`
	}

	var di deviInfo
	if err := xml.Unmarshal([]byte(soap), &di); err != nil {
		return nil, fmt.Errorf("parse DeviceInformation: %w", err)
	}

	r := di.Body.Response
	return &DeviceInfo{
		Manufacturer:    r.Manufacturer,
		Model:           r.Model,
		FirmwareVersion: r.FirmwareVersion,
		SerialNumber:    r.SerialNumber,
		HardwareID:      r.HardwareId,
	}, nil
}

// GetSnapshotURI returns a temporary JPEG URL for the camera and its profiles.
func GetSnapshotURI(xaddr, username, password string) (string, error) {
	u, err := url.Parse(xaddr)
	if err != nil {
		return "", err
	}
	dev, err := ritjonvif.NewDevice(ritjonvif.DeviceParams{
		Xaddr:    fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		Username: username,
		Password: password,
	})
	if err != nil {
		return "", err
	}

	// Fetch profiles to get the token for main stream
	profilesResp, err := dev.CallMethod(media.GetProfiles{})
	if err != nil {
		return "", err
	}
	soap := gosoap.SoapMessage(readBody(profilesResp))
	var mp mediaProfiles
	if err := xml.Unmarshal([]byte(soap), &mp); err != nil {
		return "", err
	}
	if len(mp.Body.GetProfilesResponse.Profiles) == 0 {
		return "", fmt.Errorf("no profiles found")
	}
	mainIdx, _ := classifyProfiles(mp.Body.GetProfilesResponse.Profiles)
	token := mp.Body.GetProfilesResponse.Profiles[mainIdx].Token

	// Fetch snapshot URI
	snapRes, err := dev.CallMethod(media.GetSnapshotUri{
		ProfileToken: usexonvif.ReferenceToken(token),
	})
	if err != nil {
		return "", err
	}

	type snapEnv struct {
		XMLName xml.Name `xml:"Envelope"`
		Body    struct {
			GetSnapshotUriResponse struct {
				MediaUri struct {
					URI string `xml:"Uri"`
				} `xml:"MediaUri"`
			} `xml:"GetSnapshotUriResponse"`
		} `xml:"Body"`
	}
	var se snapEnv
	if err := xml.Unmarshal([]byte(gosoap.SoapMessage(readBody(snapRes))), &se); err != nil {
		return "", err
	}

	snapURI := se.Body.GetSnapshotUriResponse.MediaUri.URI
	if snapURI == "" {
		return "", fmt.Errorf("device did not return a snapshot URI")
	}

	// Inject credentials if URL doesn't have them
	pu, err := url.Parse(snapURI)
	if err == nil && pu.User == nil {
		pu.User = url.UserPassword(username, password)
		return pu.String(), nil
	}

	return snapURI, nil
}

// PTZMove sends a continuous move command to the camera.
// pan, tilt, zoom are values from -1.0 to 1.0.
func PTZMove(xaddr, username, password string, pan, tilt, zoom float64) error {
	u, err := url.Parse(xaddr)
	if err != nil {
		return err
	}
	dev, err := ritjonvif.NewDevice(ritjonvif.DeviceParams{
		Xaddr:    fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		Username: username,
		Password: password,
	})
	if err != nil {
		return err
	}

	// Fetch primary profile token
	profilesResp, err := dev.CallMethod(media.GetProfiles{})
	if err != nil {
		return err
	}
	var mp mediaProfiles
	if err := xml.Unmarshal([]byte(gosoap.SoapMessage(readBody(profilesResp))), &mp); err != nil {
		return err
	}
	if len(mp.Body.GetProfilesResponse.Profiles) == 0 {
		return fmt.Errorf("no profiles found for ptz")
	}
	mainIdx, _ := classifyProfiles(mp.Body.GetProfilesResponse.Profiles)
	token := mp.Body.GetProfilesResponse.Profiles[mainIdx].Token

	// Send Continuous Move
	moveReq := ptz.ContinuousMove{
		ProfileToken: ritjxonvif.ReferenceToken(token),
		Velocity: ritjxonvif.PTZSpeed{
			PanTilt: ritjxonvif.Vector2D{
				X: pan,
				Y: tilt,
			},
			Zoom: ritjxonvif.Vector1D{
				X: zoom,
			},
		},
	}
	_, err = dev.CallMethod(moveReq)
	return err
}

// PTZStop stops any ongoing PTZ movement.
func PTZStop(xaddr, username, password string) error {
	u, err := url.Parse(xaddr)
	if err != nil {
		return err
	}
	dev, err := ritjonvif.NewDevice(ritjonvif.DeviceParams{
		Xaddr:    fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		Username: username,
		Password: password,
	})
	if err != nil {
		return err
	}

	// Fetch primary profile token
	profilesResp, err := dev.CallMethod(media.GetProfiles{})
	if err != nil {
		return err
	}
	var mp mediaProfiles
	if err := xml.Unmarshal([]byte(gosoap.SoapMessage(readBody(profilesResp))), &mp); err != nil {
		return err
	}
	if len(mp.Body.GetProfilesResponse.Profiles) == 0 {
		return fmt.Errorf("no profiles found for ptz")
	}
	mainIdx, _ := classifyProfiles(mp.Body.GetProfilesResponse.Profiles)
	token := mp.Body.GetProfilesResponse.Profiles[mainIdx].Token

	stopReq := ptz.Stop{
		ProfileToken: ritjxonvif.ReferenceToken(token),
		PanTilt:      xsd.Boolean(true),
		Zoom:         xsd.Boolean(true),
	}
	_, err = dev.CallMethod(stopReq)
	return err
}

// Reboot triggers a system reboot on the camera.
func Reboot(xaddr, username, password string) error {
	u, err := url.Parse(xaddr)
	if err != nil {
		return err
	}
	dev, err := ritjonvif.NewDevice(ritjonvif.DeviceParams{
		Xaddr:    fmt.Sprintf("%s:%s", u.Hostname(), u.Port()),
		Username: username,
		Password: password,
	})
	if err != nil {
		return err
	}
	_, err = dev.CallMethod(device.SystemReboot{})
	return err
}

// ────────────────────────────────────────────────────────────────────────────
// Internal helpers
// ────────────────────────────────────────────────────────────────────────────

type mediaProfiles struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		GetProfilesResponse struct {
			Profiles []cProfile `xml:"Profiles"`
		} `xml:"GetProfilesResponse"`
	} `xml:"Body"`
}

type cProfile struct {
	Token                    string `xml:"token,attr"`
	Name                     string `xml:"Name"`
	VideoSourceConfiguration struct {
		SourceToken string `xml:"SourceToken"`
	} `xml:"VideoSourceConfiguration"`
	VideoEncoderConfiguration struct {
		Resolution struct {
			Width  int `xml:"Width"`
			Height int `xml:"Height"`
		} `xml:"Resolution"`
	} `xml:"VideoEncoderConfiguration"`
	PTZConfiguration *struct {
		Token string `xml:"token,attr"`
	} `xml:"PTZConfiguration"`
}

// classifyProfiles returns the indices of the best main (highest res) and
// sub (lowest res, different source) profiles.
func classifyProfiles(profiles []cProfile) (mainIdx, subIdx int) {
	mainIdx, subIdx = -1, -1
	maxRes := 0
	minRes := int(^uint(0) >> 1)

	// Phase 1: match by name keywords
	for i, p := range profiles {
		lower := strings.ToLower(p.Name)
		if mainIdx == -1 && (strings.Contains(lower, "main") || strings.Contains(lower, "primary") || strings.Contains(lower, "hd")) {
			mainIdx = i
		} else if subIdx == -1 && (strings.Contains(lower, "sub") || strings.Contains(lower, "secondary") || strings.Contains(lower, "sd")) {
			subIdx = i
		}
	}

	// Phase 2: fallback to resolution comparison
	if mainIdx == -1 || subIdx == -1 {
		usedSrc := map[string]bool{}
		for i, p := range profiles {
			res := p.VideoEncoderConfiguration.Resolution.Width * p.VideoEncoderConfiguration.Resolution.Height
			src := p.VideoSourceConfiguration.SourceToken
			if !usedSrc[src] {
				if mainIdx == -1 && res > maxRes {
					maxRes = res
					mainIdx = i
					usedSrc[src] = true
				} else if mainIdx != -1 && subIdx == -1 && i != mainIdx && res < minRes {
					minRes = res
					subIdx = i
					usedSrc[src] = true
				}
			}
		}
		// If only one profile found, use it as main only
		if mainIdx != -1 && subIdx == -1 {
			for i, p := range profiles {
				if i != mainIdx {
					res := p.VideoEncoderConfiguration.Resolution.Width * p.VideoEncoderConfiguration.Resolution.Height
					if res < minRes {
						minRes = res
						subIdx = i
					}
				}
			}
		}
	}
	return mainIdx, subIdx
}

func fetchStreamURI(dev *ritjonvif.Device, profileToken string) (string, error) {
	req := media.GetStreamUri{
		ProfileToken: usexonvif.ReferenceToken(profileToken),
		StreamSetup: usexonvif.StreamSetup{
			Stream: usexonvif.StreamType("RTP-Unicast"),
			Transport: usexonvif.Transport{
				Protocol: "RTSP",
			},
		},
	}
	resp, err := dev.CallMethod(req)
	if err != nil {
		return "", fmt.Errorf("GetStreamUri: %w", err)
	}
	soap := gosoap.SoapMessage(readBody(resp))

	type streamURIEnvelope struct {
		XMLName xml.Name `xml:"Envelope"`
		Body    struct {
			GetStreamUriResponse struct {
				MediaUri struct {
					Uri string `xml:"Uri"`
				} `xml:"MediaUri"`
			} `xml:"GetStreamUriResponse"`
		} `xml:"Body"`
	}
	var su streamURIEnvelope
	if err := xml.Unmarshal([]byte(soap), &su); err != nil {
		return "", fmt.Errorf("parse StreamUri: %w", err)
	}
	return su.Body.GetStreamUriResponse.MediaUri.Uri, nil
}

func normalizeHost(rtspURL, deviceHostname string) string {
	u, err := url.Parse(rtspURL)
	if err != nil {
		return rtspURL
	}
	if u.Hostname() != deviceHostname {
		return strings.ReplaceAll(rtspURL, u.Hostname(), deviceHostname)
	}
	return rtspURL
}

func readBody(resp *http.Response) string {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
