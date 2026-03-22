package stream

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"go-cam-server/internal/tracectx"
)

const ingestBufSize = 64

// liveStream is the concrete Stream implementation.
//
// One goroutine (the "pump") reads from the ingest channel and fans out
// to every subscriber's Deliver() concurrently.
//
// This mirrors the C++ Engine task-queue pattern but scoped to a single
// camera stream: instead of a global worker pool dispatching any task,
// each stream has its own dedicated pump goroutine.
type liveStream struct {
	key       string
	publisher Publisher

	mu   sync.RWMutex
	subs map[string]Subscriber

	ingest chan *AVPacket
	done   chan struct{}

	packetsTotal atomic.Uint64
	startedAt    time.Time
	trace        tracectx.Context
	closed       bool
	closedMu     sync.Mutex
}

func newLiveStream(key string, pub Publisher) *liveStream {
	s := &liveStream{
		key:       key,
		publisher: pub,
		subs:      make(map[string]Subscriber),
		ingest:    make(chan *AVPacket, ingestBufSize),
		done:      make(chan struct{}),
		startedAt: time.Now(),
		trace:     traceContextFromPublisher(pub),
	}
	go s.pump()
	return s
}

// pump is the single goroutine that reads from the ingest channel and
// forwards each packet to all registered subscribers (non-blocking fan-out).
//
// Slow subscribers drop packets (their Deliver() is non-blocking) rather
// than slowing the pump — same guarantee as the C++ task queue.
func (s *liveStream) pump() {
	for {
		select {
		case pkt, ok := <-s.ingest:
			if !ok {
				return
			}
			s.packetsTotal.Add(1)
			s.mu.RLock()
			for _, sub := range s.subs {
				pkt.Retain()
				sub.Deliver(pkt)
			}
			s.mu.RUnlock()
			pkt.Release()
		case <-s.done:
			return
		}
	}
}

func (s *liveStream) Key() string         { return s.key }
func (s *liveStream) Publisher() Publisher { return s.publisher }

func (s *liveStream) IsLive() bool {
	s.closedMu.Lock()
	defer s.closedMu.Unlock()
	return !s.closed
}

// Ingest enqueues a packet into the stream pipeline.
// Non-blocking: drops silently if the ingest buffer is saturated.
func (s *liveStream) Ingest(pkt *AVPacket) {
	s.closedMu.Lock()
	closed := s.closed
	s.closedMu.Unlock()
	if closed {
		return
	}
	select {
	case s.ingest <- pkt:
	default:
		logrus.WithFields(streamFields(s.key, s.publisher.NodeID(), s.trace)).Warn("stream.ingest_buffer_full")
	}
}

func (s *liveStream) AddSubscriber(sub Subscriber) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.ID()] = sub
	fields := streamFields(s.key, s.publisher.NodeID(), s.trace)
	fields["subscriber_id"] = sub.ID()
	fields["subscriber_type"] = sub.Type().String()
	logrus.WithFields(fields).Info("stream.subscriber_added")
	return nil
}

func (s *liveStream) RemoveSubscriber(id string) {
	s.mu.Lock()
	sub, ok := s.subs[id]
	if ok {
		sub.Close()
		delete(s.subs, id)
	}
	s.mu.Unlock()
	if ok {
		fields := streamFields(s.key, s.publisher.NodeID(), s.trace)
		fields["subscriber_id"] = id
		logrus.WithFields(fields).Info("stream.subscriber_removed")
	}
}

func (s *liveStream) Subscribers() []Subscriber {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Subscriber, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out
}

func (s *liveStream) Close() {
	s.closedMu.Lock()
	if s.closed {
		s.closedMu.Unlock()
		return
	}
	s.closed = true
	s.closedMu.Unlock()

	s.mu.Lock()
	for _, sub := range s.subs {
		sub.Close()
	}
	s.subs = make(map[string]Subscriber)
	s.mu.Unlock()

	close(s.done)
	logrus.WithFields(streamFields(s.key, s.publisher.NodeID(), s.trace)).Info("stream.closed")
}

func (s *liveStream) Stats() StreamStats {
	s.mu.RLock()
	subCount := len(s.subs)
	s.mu.RUnlock()
	return StreamStats{
		SubscriberCount: subCount,
		PacketsTotal:    s.packetsTotal.Load(),
		StartedAt:       s.startedAt,
	}
}

func (s *liveStream) traceContext() tracectx.Context {
	return s.trace
}

func traceContextFromPublisher(pub Publisher) tracectx.Context {
	carrier, ok := pub.(TraceCarrier)
	if !ok {
		return tracectx.Context{}
	}
	return carrier.TraceContext()
}
