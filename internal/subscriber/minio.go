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
	"go-cam-server/internal/tracectx"
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
	trace       tracectx.Context

	ch      chan *stream.AVPacket
	dropped atomic.Uint64
	done    chan struct{}

	uploadCount atomic.Int64
	uploadBytes atomic.Int64
}

func NewMinIOSubscriber(streamKey string, client *vmsminio.Client, segmentSecs int, tc tracectx.Context) *MinIOSubscriber {
	if segmentSecs <= 0 {
		segmentSecs = 10
	}
	s := &MinIOSubscriber{
		id:          fmt.Sprintf("minio-%s", streamKey),
		streamKey:   streamKey,
		client:      client,
		segmentSecs: segmentSecs,
		trace:       tc,
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
		pkt.Release()
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
	defer func() {
		for {
			select {
			case pkt := <-s.ch:
				pkt.Release()
			default:
				return
			}
		}
	}()

	ticker := time.NewTicker(time.Duration(s.segmentSecs) * time.Second)
	defer ticker.Stop()

	buf := s.newBuffer()

	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			err := vmsflv.WriteTag(buf, pkt)
			pkt.Release()
			if err != nil {
				logrus.WithFields(s.fields()).WithError(err).Warnf("minio-sub[%s]: write tag", s.streamKey)
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
			fields := s.fields()
			fields["uploads"] = s.uploadCount.Load()
			logrus.WithFields(fields).Info("minio_sub.closed")
			return
		}
	}
}

func (s *MinIOSubscriber) newBuffer() *bytes.Buffer {
	buf := new(bytes.Buffer)
	if err := vmsflv.WriteFileHeader(buf); err != nil {
		logrus.WithFields(s.fields()).WithError(err).Errorf("minio-sub[%s]: write FLV header", s.streamKey)
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
		logrus.WithFields(s.fields()).WithError(err).Errorf("minio-sub[%s]: upload failed", s.streamKey)
		return
	}

	s.uploadCount.Add(1)
	s.uploadBytes.Add(int64(len(data)))

	statFields := s.fields()
	statFields["object_key"] = key
	statFields["bytes"] = len(data)
	statFields["elapsed_ms"] = elapsed.Milliseconds()
	statFields["uploads"] = s.uploadCount.Load()
	vmslogging.Stat("minio.segment_uploaded", statFields)

	slowFields := s.fields()
	slowFields["object_key"] = key
	slowFields["bytes"] = len(data)
	vmslogging.SlowIfExceeds("minio.segment_upload", elapsed, slowFields)

	infoFields := s.fields()
	infoFields["object_key"] = key
	infoFields["kilobytes"] = len(data) / 1024
	logrus.WithFields(infoFields).Info("minio_sub.uploaded")
}

func (s *MinIOSubscriber) fields() logrus.Fields {
	fields := tracectx.FieldsFromTrace(s.trace)
	fields["stream_key"] = s.streamKey
	fields["subscriber_id"] = s.id
	fields["subscriber_type"] = stream.TypeMinIO.String()
	return fields
}
