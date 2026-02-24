package stream

import (
	"fmt"
	"sync"

	"github.com/sirupsen/logrus"
)

// StreamManager is the in-process registry of all active streams.
//
// Equivalent to C++ BoxManager + UserManager combined, but keyed by stream
// name. Thread-safe for concurrent publisher/subscriber operations.
type StreamManager struct {
	mu      sync.RWMutex
	streams map[string]*liveStream
}

func NewStreamManager() *StreamManager {
	return &StreamManager{
		streams: make(map[string]*liveStream),
	}
}

// Register creates and starts a new stream for the given publisher.
// Returns an error if a stream with the same key is already live.
func (m *StreamManager) Register(pub Publisher) (Stream, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := pub.StreamKey()
	if _, exists := m.streams[key]; exists {
		return nil, fmt.Errorf("stream %q is already live", key)
	}

	s := newLiveStream(key, pub)
	m.streams[key] = s
	logrus.Infof("stream-manager: registered %s (node=%s)", key, pub.NodeID())
	return s, nil
}

// Unregister closes the stream and removes it from the registry.
func (m *StreamManager) Unregister(key string) {
	m.mu.Lock()
	s, ok := m.streams[key]
	if ok {
		delete(m.streams, key)
	}
	m.mu.Unlock()

	if ok {
		s.Close()
		logrus.Infof("stream-manager: unregistered %s", key)
	}
}

// Get returns the stream for the given key.
func (m *StreamManager) Get(key string) (Stream, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.streams[key]
	return s, ok
}

// All returns a snapshot of all currently live streams.
func (m *StreamManager) All() []Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Stream, 0, len(m.streams))
	for _, s := range m.streams {
		out = append(out, s)
	}
	return out
}

// Subscribe attaches a subscriber to an existing stream.
func (m *StreamManager) Subscribe(key string, sub Subscriber) error {
	m.mu.RLock()
	s, ok := m.streams[key]
	m.mu.RUnlock()

	if !ok {
		return fmt.Errorf("stream %q not found", key)
	}
	return s.AddSubscriber(sub)
}
