package subscriber

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sirupsen/logrus"

	"go-cam-server/internal/stream"
)

const (
	livestreamBufSize = 64
	gopCacheMax       = 256 // max packets in GOP (keyframe group) cache
)

// LivestreamSubscriber holds a GOP cache and pushes packets to a viewer callback.
//
// Design:
//   - Buffers packets in a channel (non-blocking Deliver)
//   - Sends the cached GOP first when a viewer joins mid-stream,
//     so they get a complete frame immediately (no waiting for next keyframe)
//   - The callback function decides how to send to the viewer:
//     WebSocket, SSE, gRPC stream, HTTP chunked — any transport works
//
// This mirrors the C++ User::push_receive() → Session pattern,
// but the transport is injected rather than hard-coded.
type LivestreamSubscriber struct {
	id      string
	ch      chan *stream.AVPacket
	dropped atomic.Uint64
	done    chan struct{}

	// gopCache stores the last keyframe group for late-joining viewers.
	gopMu    sync.Mutex
	gopCache []*stream.AVPacket

	// onPkt is called for each packet in the viewer's goroutine.
	// Returning an error closes the subscriber.
	onPkt func(pkt *stream.AVPacket) error
}

// NewLivestreamSubscriber creates a subscriber and starts pumping packets to onPkt.
// The id should be unique per viewer session.
func NewLivestreamSubscriber(id string, onPkt func(pkt *stream.AVPacket) error) *LivestreamSubscriber {
	s := &LivestreamSubscriber{
		id:    fmt.Sprintf("live-%s", id),
		ch:    make(chan *stream.AVPacket, livestreamBufSize),
		done:  make(chan struct{}),
		onPkt: onPkt,
	}
	go s.run()
	return s
}

func (s *LivestreamSubscriber) ID() string                  { return s.id }
func (s *LivestreamSubscriber) Type() stream.SubscriberType { return stream.TypeLivestream }
func (s *LivestreamSubscriber) DroppedPackets() uint64      { return s.dropped.Load() }

func (s *LivestreamSubscriber) Deliver(pkt *stream.AVPacket) {
	s.updateGOP(pkt)
	select {
	case s.ch <- pkt:
	default:
		s.dropped.Add(1)
		pkt.Release()
	}
}

func (s *LivestreamSubscriber) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// SendGOP sends the cached GOP immediately to the viewer.
// Call this after creating the subscriber to give the viewer an instant
// first frame without waiting for the next keyframe.
func (s *LivestreamSubscriber) SendGOP() {
	s.gopMu.Lock()
	gop := make([]*stream.AVPacket, len(s.gopCache))
	copy(gop, s.gopCache)
	s.gopMu.Unlock()

	for _, pkt := range gop {
		if err := s.onPkt(pkt); err != nil {
			logrus.Warnf("live[%s]: SendGOP error: %v", s.id, err)
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────

func (s *LivestreamSubscriber) run() {
	defer func() {
		// Drain channel to release remaining packets
		for {
			select {
			case pkt := <-s.ch:
				pkt.Release()
			default:
				// Release GOP cache on exit
				s.gopMu.Lock()
				for _, p := range s.gopCache {
					p.Release()
				}
				s.gopCache = nil
				s.gopMu.Unlock()
				return
			}
		}
	}()

	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			err := s.onPkt(pkt)
			pkt.Release()
			if err != nil {
				logrus.Infof("live[%s]: viewer disconnected: %v", s.id, err)
				return
			}
		case <-s.done:
			logrus.Infof("live[%s]: closed", s.id)
			return
		}
	}
}

// updateGOP maintains the GOP cache: resets on every video keyframe.
func (s *LivestreamSubscriber) updateGOP(pkt *stream.AVPacket) {
	s.gopMu.Lock()
	defer s.gopMu.Unlock()

	if pkt.Type == stream.PacketVideo {
		if pkt.IsKeyframe {
			for _, p := range s.gopCache {
				p.Release()
			}
			s.gopCache = s.gopCache[:0]
		}
		pkt.Retain()
		s.gopCache = append(s.gopCache, pkt)
	} else {
		// Audio and metadata belong to the current GOP
		pkt.Retain()
		s.gopCache = append(s.gopCache, pkt)
	}

	cut := len(s.gopCache) - gopCacheMax
	if cut > 0 {
		for i := 0; i < cut; i++ {
			s.gopCache[i].Release()
		}
		s.gopCache = s.gopCache[cut:]
	}
}
