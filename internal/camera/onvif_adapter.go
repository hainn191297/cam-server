// Package camera — ONVIF adapter implementation.
package camera

import (
	"context"
	"fmt"
	"strings"

	"go-cam-server/internal/onvif"
)

// ONVIFAdapter implements Adapter using the ONVIF protocol.
// It uses WS-Discovery to locate cameras on the LAN, then fetches RTSP URIs
// from the device's Media service.
type ONVIFAdapter struct{}

func NewONVIFAdapter() *ONVIFAdapter { return &ONVIFAdapter{} }

func (a *ONVIFAdapter) Name() string { return "onvif" }

// Resolve connects to the ONVIF device at params.Endpoint and returns the
// normalised CameraStream with main (and optionally sub) RTSP URIs.
func (a *ONVIFAdapter) Resolve(ctx context.Context, params ResolveParams) (*CameraStream, error) {
	if params.Endpoint == "" {
		return nil, fmt.Errorf("onvif adapter: endpoint is required")
	}

	info, err := onvif.GetStreams(params.Endpoint, params.Username, params.Password)
	if err != nil {
		return nil, fmt.Errorf("onvif adapter: resolve streams: %w", err)
	}
	if info.MainStream == "" {
		return nil, fmt.Errorf("onvif adapter: no main stream found on device %s", params.Endpoint)
	}

	cameraID := params.CameraID
	if cameraID == "" {
		// Generate a stable ID from the device endpoint hostname:port.
		cameraID = "onvif-" + sanitizeID(params.Endpoint)
	}

	streams := map[StreamQuality]string{
		QualityMain: info.MainStream,
	}
	if info.SubStream != "" {
		streams[QualitySub] = info.SubStream
	}

	return &CameraStream{
		CameraID: cameraID,
		Name:     cameraID,
		Source:   a.Name(),
		Streams:  streams,
		Capabilities: DeviceCapabilities{
			CanPTZ:      info.CanPTZ,
			CanSnapshot: info.CanSnapshot,
		},
	}, nil
}

// Discover scans the given NIC (or all NICs if empty) and returns the ONVIF
// device endpoints found. Callers can then call Resolve for each endpoint.
func (a *ONVIFAdapter) Discover(nic string) ([]onvif.DiscoveredDevice, error) {
	return onvif.Discover(nic)
}

// sanitizeID creates a safe filename/key-style ID from a URL string.
func sanitizeID(raw string) string {
	// Strip protocol
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "https://")
	// Keep only host:port (drop path)
	if idx := strings.IndexByte(raw, '/'); idx >= 0 {
		raw = raw[:idx]
	}
	// Replace non-alphanumeric chars with hyphens
	var sb strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('-')
		}
	}
	return sb.String()
}
