package subscriber

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	vmsflv "go-cam-server/internal/flv"
	vmslogging "go-cam-server/internal/logging"
	vmsminio "go-cam-server/internal/minio"
	"go-cam-server/internal/stream"
)

const minioBufSize = 512

// MinIOSubscriber buffers AVPackets in memory and uploads a segment to MinIO
// every N seconds (segmentSecs).
//
// Upload path: recordings/{streamKey}/{unix_ms}.flv
//
// Design:
//   - In-memory bytes.Buffer + FLV file writer (no local disk I/O)
//   - Ticker fires every segmentSecs → snapshot buffer → upload goroutine
//   - Upload is async: a new buffer starts immediately, the old one is uploaded
//     concurrently so the subscriber never blocks the stream pump
//   - On Close(): upload the final partial segment
type MinIOSubscriber struct {
	id          string
	streamKey   string
	client      *vmsminio.Client
	segmentSecs int

	ch      chan *stream.AVPacket
	dropped atomic.Uint64
	done    chan struct{}

	uploadCount atomic.Int64
	uploadBytes atomic.Int64
}

func NewMinIOSubscriber(streamKey string, client *vmsminio.Client, segmentSecs int) *MinIOSubscriber {
	if segmentSecs <= 0 {
		segmentSecs = 10
	}
	s := &MinIOSubscriber{
		id:          fmt.Sprintf("minio-%s", streamKey),
		streamKey:   streamKey,
		client:      client,
		segmentSecs: segmentSecs,
		ch:          make(chan *stream.AVPacket, minioBufSize),
		done:        make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *MinIOSubscriber) ID() string                  { return s.id }
func (s *MinIOSubscriber) Type() stream.SubscriberType { return stream.TypeMinIO }
func (s *MinIOSubscriber) DroppedPackets() uint64      { return s.dropped.Load() }
func (s *MinIOSubscriber) UploadCount() int64          { return s.uploadCount.Load() }
func (s *MinIOSubscriber) UploadBytes() int64          { return s.uploadBytes.Load() }

func (s *MinIOSubscriber) Deliver(pkt *stream.AVPacket) {
	select {
	case s.ch <- pkt:
	default:
		s.dropped.Add(1)
	}
}

func (s *MinIOSubscriber) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// ─────────────────────────────────────────────────────────────────────────────

func (s *MinIOSubscriber) run() {
	ticker := time.NewTicker(time.Duration(s.segmentSecs) * time.Second)
	defer ticker.Stop()

	buf := s.newBuffer()

	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			if err := vmsflv.WriteTag(buf, pkt); err != nil {
				logrus.Warnf("minio-sub[%s]: write tag: %v", s.streamKey, err)
			}

		case <-ticker.C:
			snapshot := buf.Bytes()
			if len(snapshot) > 0 {
				data := make([]byte, len(snapshot))
				copy(data, snapshot)
				go s.upload(data)
			}
			buf = s.newBuffer()

		case <-s.done:
			if data := buf.Bytes(); len(data) > 0 {
				s.upload(data)
			}
			logrus.Infof("minio-sub[%s]: closed (uploads=%d)", s.streamKey, s.uploadCount.Load())
			return
		}
	}
}

func (s *MinIOSubscriber) newBuffer() *bytes.Buffer {
	buf := new(bytes.Buffer)
	if err := vmsflv.WriteFileHeader(buf); err != nil {
		logrus.Errorf("minio-sub[%s]: write FLV header: %v", s.streamKey, err)
	}
	return buf
}

func (s *MinIOSubscriber) upload(data []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	started := time.Now()
	key, err := s.client.UploadSegment(ctx, s.streamKey, data)
	elapsed := time.Since(started)
	if err != nil {
		logrus.Errorf("minio-sub[%s]: upload failed: %v", s.streamKey, err)
		return
	}

	s.uploadCount.Add(1)
	s.uploadBytes.Add(int64(len(data)))

	vmslogging.Stat("minio.segment_uploaded", logrus.Fields{
		"stream_key": s.streamKey,
		"object_key": key,
		"bytes":      len(data),
		"elapsed_ms": elapsed.Milliseconds(),
		"uploads":    s.uploadCount.Load(),
	})

	vmslogging.SlowIfExceeds("minio.segment_upload", elapsed, logrus.Fields{
		"stream_key": s.streamKey,
		"object_key": key,
		"bytes":      len(data),
	})

	logrus.Infof("minio-sub[%s]: uploaded %s (%d KB)", s.streamKey, key, len(data)/1024)
}
