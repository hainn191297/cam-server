package subscriber

import (
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"text/template"
	"time"

	"github.com/sirupsen/logrus"

	vmsflv "go-cam-server/internal/flv"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

const hlsBufSize = 256

// HLSSubscriber segments the stream into rolling FLV files and generates
// an HLS-style m3u8 playlist.
//
// NOTE (POC): Segments are stored as .flv files, not MPEG-TS (.ts).
// This works with VLC and ffplay. For browser-native HLS:
//   - Replace the FLV writer with an MPEG-TS muxer (H.264 NAL → TS PES)
//   - Or pipe through ffmpeg: ffmpeg -i rtmp://... -hls_time 2 -f hls out.m3u8
//
// Segment naming: <hlsRoot>/<streamKey>/seg_<N>.flv
// Playlist path:  <hlsRoot>/<streamKey>/index.m3u8
// HTTP URL:       GET /hls/<streamKey>/index.m3u8  (served by api/streams.go)
type HLSSubscriber struct {
	id              string
	streamKey       string
	hlsRoot         string
	segmentDuration int // seconds per segment
	maxSegments     int // rolling window
	trace           tracectx.Context

	ch      chan *stream.AVPacket
	dropped atomic.Uint64
	done    chan struct{}
}

func NewHLSSubscriber(streamKey, hlsRoot string, segmentDuration, maxSegments int, tc tracectx.Context) *HLSSubscriber {
	s := &HLSSubscriber{
		id:              fmt.Sprintf("hls-%s", streamKey),
		streamKey:       streamKey,
		hlsRoot:         hlsRoot,
		segmentDuration: segmentDuration,
		maxSegments:     maxSegments,
		trace:           tc,
		ch:              make(chan *stream.AVPacket, hlsBufSize),
		done:            make(chan struct{}),
	}
	go s.run()
	return s
}

func (s *HLSSubscriber) ID() string                  { return s.id }
func (s *HLSSubscriber) Type() stream.SubscriberType { return stream.TypeHLS }
func (s *HLSSubscriber) DroppedPackets() uint64      { return s.dropped.Load() }

func (s *HLSSubscriber) Deliver(pkt *stream.AVPacket) {
	select {
	case s.ch <- pkt:
	default:
		s.dropped.Add(1)
		pkt.Release()
	}
}

func (s *HLSSubscriber) Close() {
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}

// ─────────────────────────────────────────────────────────────────────────────

type segment struct {
	filename string  // relative to hlsRoot/streamKey, e.g. "seg_0.flv"
	duration float64 // actual duration in seconds
}

func (s *HLSSubscriber) run() {
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

	dir := filepath.Join(s.hlsRoot, s.streamKey)
	if err := os.MkdirAll(dir, 0755); err != nil {
		logrus.WithFields(s.fields()).WithError(err).Errorf("hls[%s]: mkdir failed", s.streamKey)
		return
	}

	var (
		segs     []segment
		segIdx   int
		segFile  *os.File
		segStart time.Time
	)

	openSegment := func() error {
		if segFile != nil {
			segFile.Close()
		}
		name := fmt.Sprintf("seg_%d.flv", segIdx)
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			return err
		}
		if err := vmsflv.WriteFileHeader(f); err != nil {
			f.Close()
			return err
		}
		segFile = f
		segStart = time.Now()
		fields := s.fields()
		fields["segment"] = name
		logrus.WithFields(fields).Info("hls.segment_opened")
		return nil
	}

	closeSegment := func() segment {
		dur := time.Since(segStart).Seconds()
		name := fmt.Sprintf("seg_%d.flv", segIdx)
		if segFile != nil {
			segFile.Close()
			segFile = nil
		}
		return segment{filename: name, duration: dur}
	}

	if err := openSegment(); err != nil {
		logrus.WithFields(s.fields()).WithError(err).Errorf("hls[%s]: open first segment", s.streamKey)
		return
	}

	ticker := time.NewTicker(time.Duration(s.segmentDuration) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case pkt, ok := <-s.ch:
			if !ok {
				return
			}
			if segFile != nil {
				err := vmsflv.WriteTag(segFile, pkt)
				pkt.Release()
				if err != nil {
					logrus.WithFields(s.fields()).WithError(err).Warnf("hls[%s]: write tag error", s.streamKey)
				}
			} else {
				pkt.Release()
			}

		case <-ticker.C:
			seg := closeSegment()
			segs = append(segs, seg)
			if len(segs) > s.maxSegments {
				old := segs[0]
				_ = os.Remove(filepath.Join(dir, old.filename))
				segs = segs[1:]
			}
			segIdx++
			if err := openSegment(); err != nil {
				fields := s.fields()
				fields["segment_index"] = segIdx
				logrus.WithFields(fields).WithError(err).Errorf("hls[%s]: open segment", s.streamKey)
				return
			}
			s.writePlaylist(dir, segs)

		case <-s.done:
			seg := closeSegment()
			segs = append(segs, seg)
			s.writePlaylist(dir, segs)
			fields := s.fields()
			fields["segments"] = len(segs)
			logrus.WithFields(fields).Info("hls.closed")
			return
		}
	}
}

// writePlaylist writes the m3u8 file for the current rolling segment window.
func (s *HLSSubscriber) writePlaylist(dir string, segs []segment) {
	path := filepath.Join(dir, "index.m3u8")
	f, err := os.Create(path)
	if err != nil {
		logrus.WithFields(s.fields()).WithError(err).Warnf("hls[%s]: write playlist", s.streamKey)
		return
	}
	defer f.Close()

	targetDur := s.segmentDuration + 1

	tmpl := template.Must(template.New("m3u8").Parse(`#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:{{.TargetDuration}}
#EXT-X-MEDIA-SEQUENCE:{{.MediaSeq}}
{{- range .Segments}}
#EXTINF:{{printf "%.3f" .Duration}},
{{.Filename}}
{{- end}}
`))

	type playlistData struct {
		TargetDuration int
		MediaSeq       int
		Segments       []struct {
			Duration float64
			Filename string
		}
	}

	data := playlistData{
		TargetDuration: targetDur,
		MediaSeq:       0,
	}
	for _, seg := range segs {
		data.Segments = append(data.Segments, struct {
			Duration float64
			Filename string
		}{Duration: seg.duration, Filename: seg.filename})
	}

	if err := tmpl.Execute(f, data); err != nil {
		logrus.WithFields(s.fields()).WithError(err).Warnf("hls[%s]: template execute", s.streamKey)
	}
}

func (s *HLSSubscriber) fields() logrus.Fields {
	fields := tracectx.FieldsFromTrace(s.trace)
	fields["stream_key"] = s.streamKey
	fields["subscriber_id"] = s.id
	fields["subscriber_type"] = stream.TypeHLS.String()
	return fields
}
