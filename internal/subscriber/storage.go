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
	ch        chan *stream.AVPacket
	dropped   atomic.Uint64
	done      chan struct{}
}

func NewStorageSubscriber(streamKey, rootPath string) *StorageSubscriber {
	s := &StorageSubscriber{
		id:        fmt.Sprintf("storage-%s", streamKey),
		streamKey: streamKey,
		rootPath:  rootPath,
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
		logrus.Errorf("storage[%s]: mkdir failed: %v", s.streamKey, err)
		return
	}

	filename := filepath.Join(dir, fmt.Sprintf("%d.flv", time.Now().UnixMilli()))
	f, err := os.Create(filename)
	if err != nil {
		logrus.Errorf("storage[%s]: create file failed: %v", s.streamKey, err)
		return
	}
	defer f.Close()

	if err := vmsflv.WriteFileHeader(f); err != nil {
		logrus.Errorf("storage[%s]: write FLV header: %v", s.streamKey, err)
		return
	}

	logrus.Infof("storage[%s]: writing to %s", s.streamKey, filename)

	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			if err := vmsflv.WriteTag(f, pkt); err != nil {
				logrus.Warnf("storage[%s]: write tag error: %v", s.streamKey, err)
			}
		case <-s.done:
			logrus.Infof("storage[%s]: closed, file=%s", s.streamKey, filename)
			return
		}
	}
}
