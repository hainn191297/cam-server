// Package camera — Manual RTSP adapter implementation.
//
// Use this adapter when the camera is already known and has a fixed RTSP URL —
// no discovery needed. This is the simplest possible adapter and serves as a
// reference implementation for new adapter authors.
package camera

import (
	"context"
	"fmt"
)

// ManualAdapter implements Adapter for cameras whose RTSP URLs are configured
// manually (e.g. from environment variables, a config file, or the Admin UI).
type ManualAdapter struct{}

func NewManualAdapter() *ManualAdapter { return &ManualAdapter{} }

func (a *ManualAdapter) Name() string { return "manual" }

// Resolve simply wraps the pre-supplied ManualMain / ManualSub URLs.
// No network calls are made.
func (a *ManualAdapter) Resolve(_ context.Context, params ResolveParams) (*CameraStream, error) {
	if params.ManualMain == "" {
		return nil, fmt.Errorf("manual adapter: ManualMain is required")
	}

	cameraID := params.CameraID
	if cameraID == "" {
		return nil, fmt.Errorf("manual adapter: CameraID is required")
	}

	streams := map[StreamQuality]string{
		QualityMain: params.ManualMain,
	}
	if params.ManualSub != "" {
		streams[QualitySub] = params.ManualSub
	}

	return &CameraStream{
		CameraID:     cameraID,
		Name:         cameraID,
		Source:       a.Name(),
		Streams:      streams,
		Capabilities: DeviceCapabilities{}, // Manual RTSP doesn't support ONVIF features by default
	}, nil
}
