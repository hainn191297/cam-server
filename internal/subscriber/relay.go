package subscriber

import (
	"fmt"
	"io"
	"sync/atomic"

	"github.com/sirupsen/logrus"

	vmsflv "go-cam-server/internal/flv"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

const relayBufSize = 128

// RelaySubscriber streams AVPackets to a remote node (Node B) via HTTP as FLV.
//
// This is the Node A side of the multi-node relay:
//   - Attached to a local stream when Node B requests /relay/{key}
//   - Each Deliver() encodes the packet into a running FLV stream over HTTP
//   - Uses an in-process channel (non-blocking) to avoid blocking the pump
//   - When the HTTP connection closes (Node B disconnects), Close() is called
//
// Wire diagram:
//
//	liveStream.pump()
//	    └─► RelaySubscriber.Deliver(pkt)   ← non-blocking channel push
//	              └─► encode goroutine
//	                    └─► flv.WriteTag(w, pkt)
//	                              └─► http.ResponseWriter   ──► Node B
type RelaySubscriber struct {
	id      string
	streamKey string
	w       io.Writer
	trace   tracectx.Context
	ch      chan *stream.AVPacket
	dropped atomic.Uint64
	done    chan struct{}
}

// NewRelaySubscriber creates a subscriber that writes FLV tags to w.
// The caller must have already written the FLV file header to w before
// creating this subscriber (via flv.WriteFileHeader).
func NewRelaySubscriber(streamKey string, w io.Writer, tc tracectx.Context) *RelaySubscriber {
	s := &RelaySubscriber{
		id:        fmt.Sprintf("relay-%s", streamKey),
		streamKey: streamKey,
		w:         w,
		trace:     tc,
		ch:        make(chan *stream.AVPacket, relayBufSize),
		done:      make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *RelaySubscriber) ID() string                  { return s.id }
func (s *RelaySubscriber) Type() stream.SubscriberType { return stream.TypeRelay }
func (s *RelaySubscriber) DroppedPackets() uint64      { return s.dropped.Load() }

func (s *RelaySubscriber) Deliver(pkt *stream.AVPacket) {
	select {
	case s.ch <- pkt:
	default:
		s.dropped.Add(1)
	}
}

func (s *RelaySubscriber) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

func (s *RelaySubscriber) run() {
	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			if err := vmsflv.WriteTag(s.w, pkt); err != nil {
				// Network write failed — Node B disconnected
				logrus.WithFields(s.fields()).WithError(err).Info("relay_sub.write_failed")
				return
			}
		case <-s.done:
			logrus.WithFields(s.fields()).Info("relay_sub.closed")
			return
		}
	}
}

func (s *RelaySubscriber) fields() logrus.Fields {
	fields := tracectx.FieldsFromTrace(s.trace)
	fields["stream_key"] = s.streamKey
	fields["subscriber_id"] = s.id
	fields["subscriber_type"] = stream.TypeRelay.String()
	return fields
}
