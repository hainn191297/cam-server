package node

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	vmsflv "go-cam-server/internal/flv"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

// ─────────────────────────────────────────────────────────────────────────────
// RelayPublisher — the synthetic publisher for relayed streams on Node B.
//
// When Node B pulls a stream from Node A, it registers a new Stream in its
// local StreamManager using a RelayPublisher as the "camera".
// The rest of the pipeline (HLS, Storage, Livestream subscribers) is identical
// to a locally-pushed stream — they don't know the data came from another node.
// ─────────────────────────────────────────────────────────────────────────────

type RelayPublisher struct {
	key       string
	sourceID  string // ID of the node that has the real camera
	startedAt time.Time
	trace     tracectx.Context
	cancel    context.CancelFunc
}

func (p *RelayPublisher) StreamKey() string            { return p.key }
func (p *RelayPublisher) NodeID() string               { return p.sourceID + "(relay)" }
func (p *RelayPublisher) StartedAt() time.Time         { return p.startedAt }
func (p *RelayPublisher) Stats() stream.PublisherStats { return stream.PublisherStats{} }
func (p *RelayPublisher) Stop()                        { p.cancel() }
func (p *RelayPublisher) TraceContext() tracectx.Context {
	return p.trace
}

// ─────────────────────────────────────────────────────────────────────────────
// RelayManager — tracks active relay connections on this node.
//
// Prevents duplicate relay pulls for the same stream key.
// When a relay ends (Node A disconnects), the entry is removed so the next
// viewer request triggers a fresh relay connection.
// ─────────────────────────────────────────────────────────────────────────────

type RelayManager struct {
	mu     sync.Mutex
	active map[string]context.CancelFunc // streamKey → cancel

	registry   *Registry
	manager    *stream.StreamManager
	subFactory func(streamKey string, tc tracectx.Context) []stream.Subscriber
}

func NewRelayManager(
	registry *Registry,
	manager *stream.StreamManager,
	subFactory func(string, tracectx.Context) []stream.Subscriber,
) *RelayManager {
	return &RelayManager{
		active:     make(map[string]context.CancelFunc),
		registry:   registry,
		manager:    manager,
		subFactory: subFactory,
	}
}

// EnsureRelay starts a relay for streamKey from sourceNodeID if one isn't
// already running. Safe to call multiple times for the same key.
//
// Flow:
//  1. Look up Node A's HTTP address from registry
//  2. Spin up a goroutine that pulls FLV from GET /relay/{key} on Node A
//  3. Register a synthetic stream in local StreamManager
//  4. Attach HLS + Storage subscribers (same as a local camera)
//  5. When connection drops → unregister stream, remove from active map
func (m *RelayManager) EnsureRelay(ctx context.Context, streamKey, sourceNodeID string) error {
	m.mu.Lock()
	if _, running := m.active[streamKey]; running {
		m.mu.Unlock()
		return nil // already relaying
	}

	relayCtx := context.WithoutCancel(ctx)
	if tc, ok := tracectx.FromContext(ctx); ok {
		relayCtx = tracectx.WithContext(relayCtx, tracectx.Child(tc))
	}
	relayCtx, cancel := context.WithCancel(relayCtx)
	m.active[streamKey] = cancel
	m.mu.Unlock()

	nodes, err := m.registry.AllNodes(ctx)
	if err != nil {
		cancel()
		m.remove(streamKey)
		return fmt.Errorf("relay: list nodes: %w", err)
	}

	var sourceAddr string
	for _, n := range nodes {
		if n.ID == sourceNodeID {
			sourceAddr = n.HTTPAddr
			break
		}
	}
	if sourceAddr == "" {
		cancel()
		m.remove(streamKey)
		return fmt.Errorf("relay: node %q not found", sourceNodeID)
	}

	go m.pull(relayCtx, streamKey, sourceAddr, sourceNodeID)
	return nil
}

// IsRelaying reports whether this node is currently pulling the given stream.
func (m *RelayManager) IsRelaying(streamKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.active[streamKey]
	return ok
}

// ─────────────────────────────────────────────────────────────────────────────

// pull opens an HTTP FLV stream from sourceAddr and feeds the local manager.
func (m *RelayManager) pull(ctx context.Context, streamKey, sourceAddr, sourceNodeID string) {
	defer m.remove(streamKey)
	defer m.manager.Unregister(streamKey)

	url := "http://" + sourceAddr + "/relay/" + streamKey
	fields := relayFields(ctx, streamKey, sourceNodeID)
	fields["source_addr"] = sourceAddr
	logrus.WithFields(fields).Info("relay.pull.start")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logrus.WithFields(fields).WithError(err).Error("relay.pull.build_request_failed")
		return
	}
	if tc, ok := tracectx.FromContext(ctx); ok {
		req.Header.Set("traceparent", tc.Traceparent)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		logrus.WithFields(fields).WithError(err).Error("relay.pull.connect_failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fields["status"] = resp.StatusCode
		logrus.WithFields(fields).Error("relay.pull.upstream_status_not_ok")
		return
	}

	// Parse the incoming FLV stream using our own reader.
	reader, err := vmsflv.NewReader(resp.Body)
	if err != nil {
		logrus.WithFields(fields).WithError(err).Error("relay.pull.reader_init_failed")
		return
	}

	// Register a synthetic stream in our local StreamManager.
	pub := &RelayPublisher{
		key:       streamKey,
		sourceID:  sourceNodeID,
		startedAt: time.Now(),
		trace:     traceContextValue(ctx),
		cancel:    func() {},
	}
	liveStream, err := m.manager.Register(pub)
	if err != nil {
		logrus.WithFields(fields).WithError(err).Error("relay.pull.register_failed")
		return
	}

	// Attach the same default subscribers as a local camera.
	for _, sub := range m.subFactory(streamKey, traceContextValue(ctx)) {
		_ = liveStream.AddSubscriber(sub)
	}

	logrus.WithFields(fields).Info("relay.pull.streaming")

	// Pump AVPackets into the local stream.
	// Each call to reader.Next() already returns a freshly allocated AVPacket
	// with its own Data slice — no clone needed.
	for {
		pkt, err := reader.Next()
		if err != nil {
			logrus.WithFields(fields).WithError(err).Info("relay.pull.upstream_ended")
			return
		}
		liveStream.Ingest(pkt)
	}
}

func (m *RelayManager) remove(key string) {
	m.mu.Lock()
	delete(m.active, key)
	m.mu.Unlock()
}

func relayFields(ctx context.Context, streamKey, sourceNodeID string) logrus.Fields {
	fields := tracectx.Fields(ctx)
	fields["stream_key"] = streamKey
	if sourceNodeID != "" {
		fields["source_node_id"] = sourceNodeID
	}
	return fields
}

func traceContextValue(ctx context.Context) tracectx.Context {
	tc, _ := tracectx.FromContext(ctx)
	return tc
}
