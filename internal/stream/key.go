package stream

import "fmt"

// StreamKey is the canonical identifier for a shared stream.
// It replaces the loose stream_key string.
type StreamKey struct {
	CamID     string
	ProfileID string
}

func (k StreamKey) String() string {
	return fmt.Sprintf("%s/%s", k.CamID, k.ProfileID)
}

func (k StreamKey) IsZero() bool {
	return k.CamID == "" || k.ProfileID == ""
}
