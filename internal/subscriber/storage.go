// Package subscriber provides concrete Subscriber implementations.
//
// Each subscriber type runs in its own goroutine, reading from a buffered
// channel that the stream pump fills non-blocking.
//
//   TypeStorage   — write the full stream to an FLV file (playback archive)
//   TypeHLS       — segment the stream into HLS .flv chunks + m3u8 playlist
//   TypeLivestream — hold a GOP buffer and push to a viewer connection
package subscriber

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	vmsflv "go-cam-server/internal/flv"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

const storageBufSize = 512

// StorageSubscriber writes every AVPacket to a single continuous FLV file.
//
// Purpose: full-quality playback archive.
// File path: <rootPath>/<streamKey>/<unix_ms>.flv
//
// Buffer: 512 packets — large enough to absorb disk I/O spikes without dropping.
// If the buffer overflows, packets are dropped and counted.
type StorageSubscriber struct {
	id        string
	streamKey string
	rootPath  string
	trace     tracectx.Context
	ch        chan *stream.AVPacket
	dropped   atomic.Uint64
	done      chan struct{}
}

func NewStorageSubscriber(streamKey, rootPath string, tc tracectx.Context) *StorageSubscriber {
	s := &StorageSubscriber{
		id:        fmt.Sprintf("storage-%s", streamKey),
		streamKey: streamKey,
		rootPath:  rootPath,
		trace:     tc,
		ch:        make(chan *stream.AVPacket, storageBufSize),
		done:      make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *StorageSubscriber) ID() string                  { return s.id }
func (s *StorageSubscriber) Type() stream.SubscriberType { return stream.TypeStorage }
func (s *StorageSubscriber) DroppedPackets() uint64      { return s.dropped.Load() }

// Deliver is non-blocking: drop the packet if the buffer is full.
func (s *StorageSubscriber) Deliver(pkt *stream.AVPacket) {
	select {
	case s.ch <- pkt:
	default:
		s.dropped.Add(1)
	}
}

func (s *StorageSubscriber) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

func (s *StorageSubscriber) run() {
	dir := filepath.Join(s.rootPath, s.streamKey)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logrus.WithFields(s.fields()).WithError(err).Errorf("storage[%s]: mkdir failed", s.streamKey)
		return
	}

	filename := filepath.Join(dir, fmt.Sprintf("%d.flv", time.Now().UnixMilli()))
	f, err := os.Create(filename)
	if err != nil {
		logrus.WithFields(s.fields()).WithError(err).Errorf("storage[%s]: create file failed", s.streamKey)
		return
	}
	defer f.Close()

	if err := vmsflv.WriteFileHeader(f); err != nil {
		logrus.WithFields(s.fields()).WithError(err).Errorf("storage[%s]: write FLV header", s.streamKey)
		return
	}

	fields := s.fields()
	fields["file"] = filename
	logrus.WithFields(fields).Info("storage.opened")

	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			if err := vmsflv.WriteTag(f, pkt); err != nil {
				logrus.WithFields(s.fields()).WithError(err).Warnf("storage[%s]: write tag error", s.streamKey)
			}
		case <-s.done:
			fields := s.fields()
			fields["file"] = filename
			logrus.WithFields(fields).Info("storage.closed")
			return
		}
	}
}

func (s *StorageSubscriber) fields() logrus.Fields {
	fields := tracectx.FieldsFromTrace(s.trace)
	fields["stream_key"] = s.streamKey
	fields["subscriber_id"] = s.id
	fields["subscriber_type"] = stream.TypeStorage.String()
	return fields
}
