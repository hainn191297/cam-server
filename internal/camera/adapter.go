// Package camera defines the shared abstraction for camera source adapters.
//
// Any protocol or data source (ONVIF, manual RTSP config, CSV import, etc.)
// can implement the Adapter interface and export a CameraStream. The rest of
// the system uses only CameraStream to start RTSP ingestion — it never talks
// to the adapters directly after that.
//
// Architecture:
//
//	Adapter A (ONVIF)           ──┐
//	Adapter B (Manual RTSP)     ──┤──► CameraStream ──► IngestManager.Start()
//	Adapter C (CSV / DB import) ──┘
package camera

import "context"

// StreamQuality represents the relative quality level of an RTSP stream.
type StreamQuality string

const (
	QualityMain StreamQuality = "main" // Highest resolution, used for recording
	QualitySub  StreamQuality = "sub"  // Lower resolution, used for Live preview
)

// CameraStream is the normalised output produced by any Adapter.
// The rest of go-cam-server consumes only this type.
type CameraStream struct {
	// CameraID is a stable unique identifier for the physical camera device.
	// Adapters should produce a deterministic ID (e.g. MAC, serial number, or
	// the configured stream key).
	CameraID string `json:"camera_id"`

	// Streams maps quality level → RTSP URL.
	// At minimum, Main must be populated.
	Streams map[StreamQuality]string `json:"streams"`

	// Name is a human-readable label (optional).
	Name string `json:"name,omitempty"`

	// Source records which adapter produced this entry.
	Source string `json:"source"` // e.g. "onvif", "manual", "csv"

	// Capabilities defines which features the camera supports (e.g. PTZ, Snapshot).
	Capabilities DeviceCapabilities `json:"capabilities"`
}

// DeviceCapabilities flags which features are supported by the camera.
type DeviceCapabilities struct {
	CanPTZ      bool `json:"can_ptz"`
	CanSnapshot bool `json:"can_snapshot"`
}

// MainStream returns the primary RTSP URL, or empty string if not set.
func (c *CameraStream) MainStream() string {
	return c.Streams[QualityMain]
}

// SubStream returns the secondary RTSP URL, or empty string if not set.
func (c *CameraStream) SubStream() string {
	return c.Streams[QualitySub]
}

// Adapter is the contract that any camera source must satisfy.
// Implementations are responsible for discovering or reading camera info
// from their specific protocol/source and converting it into CameraStream(s).
type Adapter interface {
	// Name returns a short identifier for this adapter type (e.g. "onvif").
	Name() string

	// Resolve takes raw source data (e.g. a discovered endpoint + credentials)
	// and returns the normalised CameraStream with RTSP URLs populated.
	Resolve(ctx context.Context, params ResolveParams) (*CameraStream, error)
}

// ResolveParams carries the input a caller supplies to Adapter.Resolve.
// Different adapters use different subsets of these fields.
type ResolveParams struct {
	// For ONVIF: the device_service XAddr URL.
	Endpoint string `json:"endpoint"`

	// Auth credentials.
	Username string `json:"username"`
	Password string `json:"password"`

	// CameraID hint: if provided, use this as the CameraID in CameraStream.
	// Otherwise, the adapter generates one.
	CameraID string `json:"camera_id,omitempty"`

	// For manual RTSP: the pre-known main/sub stream URLs.
	ManualMain string `json:"manual_main,omitempty"`
	ManualSub  string `json:"manual_sub,omitempty"`

	// NIC to use when performing network-level discovery (ONVIF WS-Discovery).
	// Leave empty to probe all interfaces.
	NIC string `json:"nic,omitempty"`
}
