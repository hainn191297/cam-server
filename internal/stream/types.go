// Package stream defines the core pub/sub pipeline.
//
// Design mirrors the C++ cam-server Engine/BoxManager but uses native
// Go channels and goroutines instead of a shared task queue + worker pool.
//
//	Camera (RTMP push)
//	  └─► Handler.OnVideo/OnAudio
//	        └─► Stream.Ingest(pkt)       ← single writer
//	              └─► pump goroutine     ← 1 per stream
//	                    └─► Subscriber.Deliver(pkt)  ← N subscribers (non-blocking)
package stream

import (
	"time"

	"go-cam-server/internal/tracectx"
)

// PacketType classifies the media content of an AVPacket.
// Values mirror FLV tag type bytes for binary compatibility.
type PacketType uint8

const (
	PacketAudio PacketType = 0x08 // FLV audio tag
	PacketVideo PacketType = 0x09 // FLV video tag
	PacketMeta  PacketType = 0x12 // FLV script data (stream metadata)
)

// AVPacket is the atomic media unit flowing through the pub/sub pipeline.
//
// Data holds the raw FLV tag body bytes — identical to what RTMP delivers in its
// audio/video/data callbacks.  This avoids any re-encoding at ingest time.
//
// Design rationale:
//   - No dependency on any FLV/codec library at the interface level.
//   - Subscribers that write FLV use internal/flv.WriteTag to serialise.
//   - IsKeyframe lets LivestreamSubscriber detect GOP boundaries without parsing.
//   - When ingest switches from RTMP to RTSP/MediaMTX, the adapter produces the
//     same AVPacket shape from RTP/codec data.
type AVPacket struct {
	Type       PacketType
	Timestamp  uint32 // milliseconds, relative to stream start
	IsKeyframe bool   // true for video IDR/keyframe packets
	Data       []byte // raw FLV tag body (codec payload bytes)
}

// SubscriberType classifies what a subscriber does with the data.
type SubscriberType int

const (
	TypeLivestream SubscriberType = iota // viewer watching live feed
	TypeStorage                          // write FLV file to disk
	TypeMinIO                            // upload segmented FLV to MinIO/S3
	TypeHLS                              // segment stream into HLS playlist
	TypeRelay                            // forward to another cluster node
)

func (t SubscriberType) String() string {
	switch t {
	case TypeLivestream:
		return "livestream"
	case TypeStorage:
		return "storage"
	case TypeMinIO:
		return "minio"
	case TypeHLS:
		return "hls"
	case TypeRelay:
		return "relay"
	default:
		return "unknown"
	}
}

// PublisherStats holds runtime metrics for a publisher (camera).
type PublisherStats struct {
	PacketsIn     uint64
	BytesReceived uint64
	LastPacketAt  time.Time
}

// Publisher represents a camera that has connected and is pushing RTMP.
// Mirrors the C++ Box concept: a single device session with an identity.
type Publisher interface {
	StreamKey() string
	NodeID() string
	StartedAt() time.Time
	Stats() PublisherStats
	Stop()
}

// TraceCarrier is an optional interface for publishers that can provide
// correlation metadata for the lifetime of a stream session.
type TraceCarrier interface {
	TraceContext() tracectx.Context
}

// Subscriber receives AVPackets from a Stream's fan-out.
// Each implementation decides what to do with the data (save, forward, encode).
type Subscriber interface {
	ID() string
	Type() SubscriberType
	// Deliver is non-blocking: drop the packet if the internal buffer is full.
	// This protects the pump goroutine from slow subscribers (e.g., disk I/O).
	Deliver(pkt *AVPacket)
	Close()
	DroppedPackets() uint64
}

// StreamStats holds aggregate metrics for a stream.
type StreamStats struct {
	SubscriberCount int
	PacketsTotal    uint64
	StartedAt       time.Time
}

// Stream is the central entity: one publisher, N subscribers.
// Mirrors the C++ BoxManager slot for a single camera feed.
type Stream interface {
	Key() string
	Publisher() Publisher
	AddSubscriber(sub Subscriber) error
	RemoveSubscriber(id string)
	Subscribers() []Subscriber
	// Ingest enqueues a packet into the stream's ingest channel.
	Ingest(pkt *AVPacket)
	IsLive() bool
	Close()
	Stats() StreamStats
}
