package relay

import (
	"sync"
	"time"
)

type SessionState string

const (
	SessionStateCreating SessionState = "creating"
	SessionStateActive   SessionState = "active"
	SessionStateDraining SessionState = "draining"
	SessionStateClosed   SessionState = "closed"
)

type Protocol string

const (
	ProtocolWebRTC Protocol = "webrtc"
	ProtocolHLS    Protocol = "hls"
)

// Session is one viewer's connection to a shared stream via the relay gateway.
type Session struct {
	RelaySessionID string
	StreamID       string
	CamID          string
	Protocol       Protocol
	Endpoint       string
	Token          string
	TokenExpiresAt time.Time
	CreatedAt      time.Time
	LastActiveAt   time.Time

	mu    sync.RWMutex
	state SessionState
}

func (s *Session) State() SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

func (s *Session) setState(st SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = st
}

// CreateSessionRequest is sent by the Relay Orchestrator (control plane) to the gateway.
type CreateSessionRequest struct {
	RelaySessionID     string     `json:"relay_session_id"`
	StreamID           string     `json:"stream_id"`
	CamID              string     `json:"cam_id"`
	RequestedProtocols []Protocol `json:"requested_protocols"`
	TokenExpiresAt     time.Time  `json:"token_expires_at"`
}

// CreateSessionResponse is returned to the control plane.
type CreateSessionResponse struct {
	RelaySessionID string   `json:"relay_session_id"`
	Protocol       Protocol `json:"protocol"`
	Endpoint       string   `json:"endpoint"`
	Token          string   `json:"token"`
}
