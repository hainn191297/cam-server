package relay

import (
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

// Manager is the relay session registry.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session // keyed by relay_session_id

	tokens  *TokenService
	baseURL string // e.g. "http://relay.example"
}

func NewManager(tokens *TokenService, baseURL string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		tokens:   tokens,
		baseURL:  baseURL,
	}
}

// Create provisions a new relay session for a viewer.
func (m *Manager) Create(req CreateSessionRequest) (*CreateSessionResponse, error) {
	// Protocol negotiation: prefer webrtc, fallback hls
	var proto Protocol
	for _, p := range req.RequestedProtocols {
		if p == ProtocolWebRTC || p == ProtocolHLS {
			proto = p
			break
		}
	}
	if proto == "" {
		proto = ProtocolHLS // safe fallback
	}

	now := time.Now()
	sess := &Session{
		RelaySessionID: req.RelaySessionID,
		StreamID:       req.StreamID,
		CamID:          req.CamID,
		Protocol:       proto,
		Endpoint:       fmt.Sprintf("%s/relay/live/%s/%s", m.baseURL, proto, req.RelaySessionID),
		TokenExpiresAt: req.TokenExpiresAt,
		CreatedAt:      now,
		LastActiveAt:   now,
		state:          SessionStateCreating,
	}

	signed, err := m.tokens.Issue(sess)
	if err != nil {
		return nil, fmt.Errorf("relay.create: issue token: %w", err)
	}
	sess.Token = signed
	sess.setState(SessionStateActive)

	m.mu.Lock()
	m.sessions[sess.RelaySessionID] = sess
	m.mu.Unlock()

	logrus.WithFields(logrus.Fields{
		"relay_session_id": sess.RelaySessionID,
		"stream_id":        sess.StreamID,
		"cam_id":           sess.CamID,
		"protocol":         proto,
	}).Info("relay.session.create")

	return &CreateSessionResponse{
		RelaySessionID: sess.RelaySessionID,
		Protocol:       proto,
		Endpoint:       sess.Endpoint,
		Token:          signed,
	}, nil
}

// Close tears down a relay session.
func (m *Manager) Close(relaySessionID string) {
	m.mu.Lock()
	sess, ok := m.sessions[relaySessionID]
	if ok {
		sess.setState(SessionStateClosed)
		delete(m.sessions, relaySessionID)
	}
	m.mu.Unlock()

	if ok {
		logrus.WithFields(logrus.Fields{
			"relay_session_id": relaySessionID,
			"stream_id":        sess.StreamID,
		}).Info("relay.session.close")
	}
}

// Get returns a session by ID.
func (m *Manager) Get(relaySessionID string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[relaySessionID]
	return s, ok
}

// ActiveCount returns the number of active sessions.
func (m *Manager) ActiveCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// ReapIdle closes sessions idle for more than maxIdle.
func (m *Manager) ReapIdle(maxIdle time.Duration) {
	cutoff := time.Now().Add(-maxIdle)
	m.mu.Lock()
	toClose := make([]string, 0)
	for id, s := range m.sessions {
		if s.LastActiveAt.Before(cutoff) {
			toClose = append(toClose, id)
		}
	}
	for _, id := range toClose {
		delete(m.sessions, id)
	}
	m.mu.Unlock()

	for _, id := range toClose {
		logrus.WithField("relay_session_id", id).Info("relay.session.idle_timeout")
	}
}

// StartReaper runs a background goroutine that closes idle sessions.
func (m *Manager) StartReaper(idleTimeout time.Duration) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			m.ReapIdle(idleTimeout)
		}
	}()
}
