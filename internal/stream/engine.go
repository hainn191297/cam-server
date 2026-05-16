package stream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// StreamState represents the lifecycle state of a shared stream.
type StreamState string

const (
	StateRequested    StreamState = "requested"
	StateStarting     StreamState = "starting"
	StateLive         StreamState = "live"
	StateDegraded     StreamState = "degraded"
	StateReconnecting StreamState = "reconnecting"
	StateStopped      StreamState = "stopped"
)

// StreamEntry tracks one active shared stream.
type StreamEntry struct {
	StreamID  string
	Key       StreamKey
	StartedAt time.Time
	state     StreamState
	mu        sync.RWMutex
}

func (e *StreamEntry) State() StreamState {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.state
}

func (e *StreamEntry) setState(s StreamState, reason string, camID, streamID string) {
	e.mu.Lock()
	from := e.state
	e.state = s
	e.mu.Unlock()
	logrus.WithFields(logrus.Fields{
		"cam_id":     camID,
		"stream_id":  streamID,
		"from_state": from,
		"to_state":   s,
		"reason":     reason,
	}).Info("stream.lifecycle.transition")
}

// IngestStarter is implemented by rtsp.IngestManager (or a direct RTSP adapter).
type IngestStarter interface {
	Start(ctx context.Context, streamKey string) error
	Stop(streamKey string)
}

// Engine is the high-level coordinator for shared streams.
// It wraps StreamManager and IngestManager and exposes the StreamKey-based API
// required by the live-session handler.
type Engine struct {
	manager *StreamManager
	ingest  IngestStarter

	mu      sync.RWMutex
	entries map[string]*StreamEntry // keyed by StreamKey.String()
}

// NewEngine creates a StreamEngine.
func NewEngine(manager *StreamManager, ingest IngestStarter) *Engine {
	return &Engine{
		manager: manager,
		ingest:  ingest,
		entries: make(map[string]*StreamEntry),
	}
}

// EnsureStream starts a stream for the key if not already running.
// Returns stream_id and the Stream. Idempotent.
func (e *Engine) EnsureStream(ctx context.Context, key StreamKey) (string, Stream, error) {
	if key.IsZero() {
		return "", nil, errors.New("stream key: cam_id and profile_id required")
	}
	strKey := key.String()

	// Fast path: stream already live
	e.mu.RLock()
	entry, exists := e.entries[strKey]
	e.mu.RUnlock()

	if exists {
		if s, ok := e.manager.Get(strKey); ok {
			return entry.StreamID, s, nil
		}
		// Entry exists but stream died; fall through to restart
	}

	// Slow path: start ingest
	e.mu.Lock()
	// Double-check under write lock
	if entry, exists = e.entries[strKey]; exists {
		if s, ok := e.manager.Get(strKey); ok {
			e.mu.Unlock()
			return entry.StreamID, s, nil
		}
		// Dead stream; remove stale entry
		delete(e.entries, strKey)
	}

	streamID := uuid.New().String()
	entry = &StreamEntry{
		StreamID:  streamID,
		Key:       key,
		StartedAt: time.Now(),
		state:     StateRequested,
	}
	e.entries[strKey] = entry
	e.mu.Unlock()

	entry.setState(StateStarting, "ensure_stream_called", key.CamID, streamID)

	if e.ingest == nil {
		entry.setState(StateStopped, "no_ingest_configured", key.CamID, streamID)
		e.mu.Lock()
		delete(e.entries, strKey)
		e.mu.Unlock()
		return "", nil, fmt.Errorf("ingest not configured (mediamtx disabled)")
	}

	if err := e.ingest.Start(ctx, strKey); err != nil {
		entry.setState(StateStopped, fmt.Sprintf("ingest_start_failed: %v", err), key.CamID, streamID)
		e.mu.Lock()
		delete(e.entries, strKey)
		e.mu.Unlock()
		return "", nil, fmt.Errorf("start ingest: %w", err)
	}

	// Wait for the stream to appear in the manager (up to 5s)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := e.manager.Get(strKey); ok {
			entry.setState(StateLive, "first_frame_registered", key.CamID, streamID)
			return streamID, s, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	entry.setState(StateStopped, "ingest_timeout", key.CamID, streamID)
	return "", nil, fmt.Errorf("stream %s did not start within timeout", strKey)
}

// StopStream stops the ingest if there are no remaining subscribers.
func (e *Engine) StopStream(key StreamKey) {
	strKey := key.String()
	e.mu.Lock()
	entry, ok := e.entries[strKey]
	if ok {
		delete(e.entries, strKey)
	}
	e.mu.Unlock()

	if ok {
		entry.setState(StateStopped, "explicit_stop", key.CamID, entry.StreamID)
		if e.ingest != nil {
			e.ingest.Stop(strKey)
		}
	}
}

// State returns the current stream state and stream_id for a key.
func (e *Engine) State(key StreamKey) (StreamState, string, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	entry, ok := e.entries[key.String()]
	if !ok {
		return StateStopped, "", false
	}
	return entry.State(), entry.StreamID, true
}
