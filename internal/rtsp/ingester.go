// Package rtsp provides RTSP pull ingestion from MediaMTX.
//
// When a camera pushes RTMP to MediaMTX, MediaMTX fires an HTTP webhook to
// the Go server (POST /internal/on-publish). The Go server then pulls the
// stream from MediaMTX via RTSP using gortsplib, converts H264/AAC RTP
// packets into FLV-body AVPackets, and feeds the local StreamManager — the
// same fan-out pipeline used for directly-connected cameras.
//
// Flow:
//
//	Camera ──RTMP──► MediaMTX :1935
//	                     │ runOnReady webhook
//	                     ▼
//	         POST /internal/on-publish?path=cam1
//	                     │
//	        IngestManager.Start(ctx, "cam1")
//	                     │
//	        gortsplib pulls rtsp://localhost:8554/cam1
//	                     │  RTP H264 + MPEG4Audio packets
//	                     ▼
//	        decode → FLV-body → stream.AVPacket
//	                     │
//	        StreamManager.Register(pub) → fan-out pump
//	              ┌──────┼───────┬─────────┐
//	              ▼      ▼       ▼         ▼
//	           HLS  Storage  MinIO    Relay
package rtsp

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtph264"
	"github.com/bluenviron/gortsplib/v5/pkg/format/rtpmpeg4audio"
	"github.com/pion/rtp"
	"github.com/sirupsen/logrus"

	"go-cam-server/config"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/subscriber"
	"go-cam-server/internal/tracectx"
)

// ─────────────────────────────────────────────────────────────────────────────
// rtspPublisher — synthetic Publisher for RTSP-ingested streams.
// ─────────────────────────────────────────────────────────────────────────────

type rtspPublisher struct {
	key       string
	nodeID    string
	startedAt time.Time
	trace     tracectx.Context
}

func (p *rtspPublisher) StreamKey() string            { return p.key }
func (p *rtspPublisher) NodeID() string               { return p.nodeID }
func (p *rtspPublisher) StartedAt() time.Time         { return p.startedAt }
func (p *rtspPublisher) Stats() stream.PublisherStats { return stream.PublisherStats{} }
func (p *rtspPublisher) Stop()                        {}
func (p *rtspPublisher) TraceContext() tracectx.Context {
	return p.trace
}

// ─────────────────────────────────────────────────────────────────────────────
// IngestManager — tracks active RTSP pull sessions, one per stream key.
// ─────────────────────────────────────────────────────────────────────────────

type IngestManager struct {
	mu         sync.Mutex
	sessions   map[string]context.CancelFunc

	manager    *stream.StreamManager
	subFactory *subscriber.Factory
	cfg        *config.Config
}

func NewIngestManager(
	manager *stream.StreamManager,
	subFactory *subscriber.Factory,
	cfg *config.Config,
) *IngestManager {
	return &IngestManager{
		sessions:   make(map[string]context.CancelFunc),
		manager:    manager,
		subFactory: subFactory,
		cfg:        cfg,
	}
}

// Start begins RTSP pull ingestion for streamKey if not already running.
// Safe to call multiple times for the same key (idempotent).
func (m *IngestManager) Start(ctx context.Context, streamKey string) error {
	m.mu.Lock()
	if _, ok := m.sessions[streamKey]; ok {
		m.mu.Unlock()
		return nil // already ingesting
	}
	ingCtx := context.WithoutCancel(ctx)
	if tc, ok := tracectx.FromContext(ctx); ok {
		ingCtx = tracectx.WithContext(ingCtx, tracectx.Child(tc))
	}
	ingCtx, cancel := context.WithCancel(ingCtx)
	m.sessions[streamKey] = cancel
	m.mu.Unlock()

	go m.ingest(ingCtx, streamKey)
	return nil
}

// Stop cancels the RTSP pull for streamKey.
func (m *IngestManager) Stop(streamKey string) {
	m.mu.Lock()
	cancel, ok := m.sessions[streamKey]
	if ok {
		delete(m.sessions, streamKey)
		cancel()
	}
	m.mu.Unlock()
}

func (m *IngestManager) remove(streamKey string) {
	m.mu.Lock()
	delete(m.sessions, streamKey)
	m.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// ingest runs in a goroutine per stream key.
// ─────────────────────────────────────────────────────────────────────────────

func (m *IngestManager) ingest(ctx context.Context, streamKey string) {
	defer m.manager.Unregister(streamKey)
	defer m.remove(streamKey)

	rtspURL := m.cfg.MediaMTX.RTSPBaseURL + "/" + streamKey
	logrus.WithFields(traceFields(ctx, streamKey)).Infof("rtsp-ingest[%s]: pulling from %s", streamKey, rtspURL)

	u, err := base.ParseURL(rtspURL)
	if err != nil {
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: parse URL", streamKey)
		return
	}

	client := &gortsplib.Client{
		Scheme: u.Scheme,
		Host:   u.Host,
	}
	if err = client.Start(); err != nil {
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: connect", streamKey)
		return
	}
	defer client.Close()

	desc, _, err := client.Describe(u)
	if err != nil {
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: describe", streamKey)
		return
	}

	// Find H264 video and MPEG4Audio formats.
	var h264fmt *format.H264
	var aacFmt *format.MPEG4Audio
	var videoMed *description.Media
	var audioMed *description.Media

	if medi := desc.FindFormat(&h264fmt); medi != nil {
		videoMed = medi
	}
	if medi := desc.FindFormat(&aacFmt); medi != nil {
		audioMed = medi
	}
	if videoMed == nil && audioMed == nil {
		logrus.WithFields(traceFields(ctx, streamKey)).Errorf("rtsp-ingest[%s]: no supported formats (need H264 or MPEG4Audio)", streamKey)
		return
	}

	var medias []*description.Media
	if videoMed != nil {
		medias = append(medias, videoMed)
	}
	if audioMed != nil {
		medias = append(medias, audioMed)
	}

	if err = client.SetupAll(desc.BaseURL, medias); err != nil {
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: setup", streamKey)
		return
	}

	// Register a synthetic stream in the local StreamManager.
	pub := &rtspPublisher{
		key:       streamKey,
		nodeID:    m.cfg.Node.ID,
		startedAt: time.Now(),
		trace:     traceContextValue(ctx),
	}
	liveStream, err := m.manager.Register(pub)
	if err != nil {
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: register stream", streamKey)
		return
	}

	// Attach default subscribers (HLS, Storage, MinIO).
	for _, sub := range m.subFactory.PublishSubscribers(streamKey, m.cfg, traceContextValue(ctx)) {
		_ = liveStream.AddSubscriber(sub)
	}

	// Emit video sequence header from SDP params (before Play, so subscribers
	// receive the decoder config before any coded frames arrive).
	if h264fmt != nil {
		sps, pps := h264fmt.SafeParams()
		if len(sps) >= 4 && len(pps) > 0 {
			pkt := buildVideoSeqHeader(sps, pps)
			liveStream.Ingest(pkt)
		}
	}

	// Emit audio sequence header from SDP config.
	if aacFmt != nil && aacFmt.Config != nil {
		configBytes, err := aacFmt.Config.Marshal()
		if err == nil && len(configBytes) > 0 {
			pkt := buildAudioSeqHeader(configBytes)
			liveStream.Ingest(pkt)
		}
	}

	// ── Video RTP callback ────────────────────────────────────────────────────
	if h264fmt != nil {
		dec, err := h264fmt.CreateDecoder()
		if err != nil {
			logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: create H264 decoder", streamKey)
			return
		}

		// These vars are only accessed from this one callback goroutine.
		var firstTS uint32
		var gotFirstTS bool
		var currentSPS, currentPPS []byte
		currentSPS, currentPPS = h264fmt.SafeParams()

		client.OnPacketRTP(videoMed, h264fmt, func(pkt *rtp.Packet) {
			nalus, decErr := dec.Decode(pkt)
			if decErr != nil {
				if !errors.Is(decErr, rtph264.ErrMorePacketsNeeded) &&
					!errors.Is(decErr, rtph264.ErrNonStartingPacketAndNoPrevious) {
					logrus.WithFields(traceFields(ctx, streamKey)).WithError(decErr).Debugf("rtsp-ingest[%s]: H264 decode", streamKey)
				}
				return
			}
			if len(nalus) == 0 {
				return
			}

			// Detect in-stream SPS/PPS updates and re-emit sequence header.
			newSPS, newPPS := extractSPSPPS(nalus)
			if newSPS != nil || newPPS != nil {
				if newSPS != nil {
					currentSPS = newSPS
				}
				if newPPS != nil {
					currentPPS = newPPS
				}
				if len(currentSPS) >= 4 && len(currentPPS) > 0 {
					pkt := buildVideoSeqHeader(currentSPS, currentPPS)
					liveStream.Ingest(pkt)
				}
			}

			outPkt := buildVideoFrame(nalus)
			if outPkt == nil {
				return
			}

			if !gotFirstTS {
				firstTS = pkt.Timestamp
				gotFirstTS = true
			}
			// H264 clock rate = 90 000 Hz → divide by 90 to get milliseconds.
			ts := (pkt.Timestamp - firstTS) / 90

			outPkt.Timestamp = ts
			liveStream.Ingest(outPkt)
		})
	}

	// ── Audio RTP callback ────────────────────────────────────────────────────
	if aacFmt != nil {
		dec, err := aacFmt.CreateDecoder()
		if err != nil {
			logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: create AAC decoder", streamKey)
			return
		}

		sampleRate := 44100
		if aacFmt.Config != nil && aacFmt.Config.SampleRate > 0 {
			sampleRate = aacFmt.Config.SampleRate
		}

		var firstTS uint32
		var gotFirstTS bool

		client.OnPacketRTP(audioMed, aacFmt, func(pkt *rtp.Packet) {
			aus, decErr := dec.Decode(pkt)
			if decErr != nil {
				if !errors.Is(decErr, rtpmpeg4audio.ErrMorePacketsNeeded) {
					logrus.WithFields(traceFields(ctx, streamKey)).WithError(decErr).Debugf("rtsp-ingest[%s]: AAC decode", streamKey)
				}
				return
			}
			if len(aus) == 0 {
				return
			}

			if !gotFirstTS {
				firstTS = pkt.Timestamp
				gotFirstTS = true
			}
			// Convert RTP timestamp (at sampleRate Hz) to milliseconds.
			ts := uint32(uint64(pkt.Timestamp-firstTS) * 1000 / uint64(sampleRate))

			for _, au := range aus {
				outPkt := buildAudioFrame(au)
				outPkt.Timestamp = ts
				liveStream.Ingest(outPkt)
			}
		})
	}

	// Start the RTSP stream.
	if _, err = client.Play(nil); err != nil {
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Errorf("rtsp-ingest[%s]: play", streamKey)
		return
	}
	logrus.WithFields(traceFields(ctx, streamKey)).Infof("rtsp-ingest[%s]: streaming", streamKey)

	// Block until context is cancelled (on-unpublish) or upstream ends.
	waitDone := make(chan error, 1)
	go func() { waitDone <- client.Wait() }()

	select {
	case <-ctx.Done():
		logrus.WithFields(traceFields(ctx, streamKey)).Infof("rtsp-ingest[%s]: stopped (context cancelled)", streamKey)
	case err = <-waitDone:
		logrus.WithFields(traceFields(ctx, streamKey)).WithError(err).Infof("rtsp-ingest[%s]: upstream ended", streamKey)
	}
}

func traceFields(ctx context.Context, streamKey string) logrus.Fields {
	fields := tracectx.Fields(ctx)
	fields["stream_key"] = streamKey
	return fields
}

func traceContextValue(ctx context.Context) tracectx.Context {
	tc, _ := tracectx.FromContext(ctx)
	return tc
}

// ─────────────────────────────────────────────────────────────────────────────
// FLV body builders
// ─────────────────────────────────────────────────────────────────────────────

// buildVideoSeqHeader builds an FLV video tag body containing the
// AVCDecoderConfigurationRecord (SPS + PPS).
//
// Layout: 0x17 0x00 0x00 0x00 0x00 + AVCDecoderConfigurationRecord
func buildVideoSeqHeader(sps, pps []byte) *stream.AVPacket {
	record := buildAVCConfigRecord(sps, pps)
	pkt := stream.NewAVPacket(5 + len(record))
	pkt.Type = stream.PacketVideo
	pkt.Timestamp = 0
	pkt.IsKeyframe = true
	pkt.Data[0] = 0x17 // keyframe | AVC codec (1<<4 | 7)
	pkt.Data[1] = 0x00 // AVC sequence header
	// pkt.Data[2:5] = composition time = 0 (already zero)
	copy(pkt.Data[5:], record)
	return pkt
}

// buildAVCConfigRecord builds an AVCDecoderConfigurationRecord from raw SPS/PPS.
// Caller must ensure len(sps) >= 4.
func buildAVCConfigRecord(sps, pps []byte) []byte {
	buf := make([]byte, 0, 11+len(sps)+len(pps))
	buf = append(buf, 0x01)                   // configurationVersion = 1
	buf = append(buf, sps[1], sps[2], sps[3]) // AVCProfileIndication, profile_compat, AVCLevelIndication
	buf = append(buf, 0xFF)                   // 6 reserved bits | lengthSizeMinusOne = 3
	buf = append(buf, 0xE1)                   // 3 reserved bits | numSequenceParameterSets = 1
	buf = append(buf, byte(len(sps)>>8), byte(len(sps)))
	buf = append(buf, sps...)
	buf = append(buf, 0x01) // numPictureParameterSets = 1
	buf = append(buf, byte(len(pps)>>8), byte(len(pps)))
	buf = append(buf, pps...)
	return buf
}

// buildVideoFrame builds an FLV video tag body for a coded frame.
//
// Layout: (0x17|0x27) 0x01 [cts 3 bytes] + [4-byte-len + NALU ...]
//
// SPS (type 7) and PPS (type 8) NALUs are filtered out — they live in the
// sequence header packet. AUD (type 9) is also filtered.
func buildVideoFrame(nalus [][]byte) *stream.AVPacket {
	var filtered [][]byte
	var isKeyframe bool
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 5: // IDR — keyframe
			isKeyframe = true
			filtered = append(filtered, nalu)
		case 7, 8, 9: // SPS, PPS, AUD — skip (handled separately)
		default:
			filtered = append(filtered, nalu)
		}
	}
	if len(filtered) == 0 {
		return nil
	}

	// Compute total body size: 5 header bytes + (4+naluLen) per NALU.
	total := 5
	for _, nalu := range filtered {
		total += 4 + len(nalu)
	}

	pkt := stream.NewAVPacket(total)
	pkt.Type = stream.PacketVideo
	pkt.IsKeyframe = isKeyframe

	if isKeyframe {
		pkt.Data[0] = 0x17 // keyframe | AVC
	} else {
		pkt.Data[0] = 0x27 // inter | AVC
	}
	pkt.Data[1] = 0x01 // AVC NALU
	// pkt.Data[2:5] = composition time = 0 (already zero)

	pos := 5
	for _, nalu := range filtered {
		binary.BigEndian.PutUint32(pkt.Data[pos:], uint32(len(nalu)))
		pos += 4
		copy(pkt.Data[pos:], nalu)
		pos += len(nalu)
	}
	return pkt
}

// buildAudioSeqHeader builds an FLV audio tag body for AudioSpecificConfig.
//
// Layout: 0xAF 0x00 + AudioSpecificConfig bytes
//
// 0xAF = SoundFormat(AAC=10) | SoundRate(44kHz=3) | SoundSize(16bit=1) | SoundType(stereo=1)
func buildAudioSeqHeader(configBytes []byte) *stream.AVPacket {
	pkt := stream.NewAVPacket(2 + len(configBytes))
	pkt.Type = stream.PacketAudio
	pkt.Timestamp = 0
	pkt.Data[0] = 0xAF // (10<<4) | (3<<2) | (1<<1) | 1
	pkt.Data[1] = 0x00 // AAC sequence header
	copy(pkt.Data[2:], configBytes)
	return pkt
}

// buildAudioFrame builds an FLV audio tag body for a raw AAC-ES frame.
//
// Layout: 0xAF 0x01 + raw AAC-ES bytes
func buildAudioFrame(data []byte) *stream.AVPacket {
	pkt := stream.NewAVPacket(2 + len(data))
	pkt.Type = stream.PacketAudio
	pkt.Data[0] = 0xAF // same codec/rate/size/channels byte as sequence header
	pkt.Data[1] = 0x01 // AAC raw
	copy(pkt.Data[2:], data)
	return pkt
}

// extractSPSPPS scans an access unit for in-band SPS and PPS NALUs.
// Returns nil slices if none found.
func extractSPSPPS(nalus [][]byte) (sps, pps []byte) {
	for _, nalu := range nalus {
		if len(nalu) == 0 {
			continue
		}
		switch nalu[0] & 0x1F {
		case 7: // SPS
			sps = nalu
		case 8: // PPS
			pps = nalu
		}
	}
	return sps, pps
}
