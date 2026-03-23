package pionbridge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/google/uuid"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"github.com/sirupsen/logrus"

	"go-cam-server/config"
)

const maxSessions = 100 // Hard limit for concurrent WebRTC sessions

type Service struct {
	cfg config.PionWebRTCConfig

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	id        string
	streamKey string
	pc        *webrtc.PeerConnection
	rtsp      *gortsplib.Client
	closed    atomic.Bool
}

type selectedTrack struct {
	media *description.Media
	forma format.Format
	track *webrtc.TrackLocalStaticRTP
}

func New(cfg config.PionWebRTCConfig) *Service {
	if !cfg.Enabled {
		return nil
	}

	if cfg.SourceRTSPBaseURL == "" {
		cfg.SourceRTSPBaseURL = "rtsp://localhost:8554"
	}
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "http://localhost:8080"
	}

	return &Service{
		cfg:      cfg,
		sessions: make(map[string]*session),
	}
}

func (s *Service) PublicOfferURL(streamKey string) string {
	key := strings.Trim(strings.TrimSpace(streamKey), "/")
	baseURL := strings.TrimRight(strings.TrimSpace(s.cfg.PublicBaseURL), "/")
	return baseURL + "/pion/webrtc/" + key + "/offer"
}

func (s *Service) HandleOffer(ctx context.Context, streamKey, offerSDP string) (sessionID string, answerSDP string, err error) {
	key := strings.Trim(strings.TrimSpace(streamKey), "/")
	if key == "" {
		return "", "", fmt.Errorf("stream key is required")
	}
	if strings.TrimSpace(offerSDP) == "" {
		return "", "", fmt.Errorf("offer sdp is required")
	}

	s.mu.Lock()
	if len(s.sessions) >= maxSessions {
		s.mu.Unlock()
		return "", "", fmt.Errorf("server at maximum capacity for webrtc sessions")
	}
	s.mu.Unlock()

	rtspURL := strings.TrimRight(s.cfg.SourceRTSPBaseURL, "/") + "/" + key
	u, err := base.ParseURL(rtspURL)
	if err != nil {
		return "", "", err
	}

	client := &gortsplib.Client{
		Scheme: u.Scheme,
		Host:   u.Host,
	}
	if err = client.Start(); err != nil {
		return "", "", err
	}

	desc, _, err := client.Describe(u)
	if err != nil {
		client.Close()
		return "", "", err
	}

	tracks, medias, err := selectTracks(desc)
	if err != nil {
		client.Close()
		return "", "", err
	}

	if err = client.SetupAll(desc.BaseURL, medias); err != nil {
		client.Close()
		return "", "", err
	}

	pcCfg := webrtc.Configuration{}
	if len(s.cfg.ICEServers) > 0 {
		pcCfg.ICEServers = append(pcCfg.ICEServers, webrtc.ICEServer{URLs: s.cfg.ICEServers})
	}
	pc, err := webrtc.NewPeerConnection(pcCfg)
	if err != nil {
		client.Close()
		return "", "", err
	}

	senders := make([]*webrtc.RTPSender, 0, len(tracks))
	for _, tr := range tracks {
		sender, addErr := pc.AddTrack(tr.track)
		if addErr != nil {
			_ = pc.Close()
			client.Close()
			return "", "", addErr
		}
		senders = append(senders, sender)
	}

	// Drain RTCP from remote (browser) using a context to avoid leaking goroutines
	ctxCancel, cancel := context.WithCancel(context.Background())
	for _, sender := range senders {
		go drainRTCP(ctxCancel, sender)
	}

	for _, tr := range tracks {
		trLocal := tr
		client.OnPacketRTP(trLocal.media, trLocal.forma, func(pkt *rtp.Packet) {
			if writeErr := trLocal.track.WriteRTP(pkt); writeErr != nil {
				// Don't log closed track errors repeatedly
				if !isExpectedCloseError(writeErr) {
					logrus.Debugf("pion-bridge[%s]: write rtp: %v", key, writeErr)
				}
			}
		})
	}

	if _, err = client.Play(nil); err != nil {
		cancel()
		_ = pc.Close()
		client.Close()
		return "", "", err
	}

	if err = pc.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offerSDP,
	}); err != nil {
		cancel()
		_ = pc.Close()
		client.Close()
		return "", "", err
	}

	gatherComplete := webrtc.GatheringCompletePromise(pc)
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		cancel()
		_ = pc.Close()
		client.Close()
		return "", "", err
	}
	if err = pc.SetLocalDescription(answer); err != nil {
		cancel()
		_ = pc.Close()
		client.Close()
		return "", "", err
	}

	// Wait for ICE gathering with a timeout using the provided request context
	select {
	case <-gatherComplete:
	case <-ctx.Done():
		cancel()
		_ = pc.Close()
		client.Close()
		return "", "", fmt.Errorf("ice gathering aborted: %w", ctx.Err())
	}

	id := uuid.NewString()
	ss := &session{
		id:        id,
		streamKey: key,
		pc:        pc,
		rtsp:      client,
	}
	s.putSession(ss)

	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		if st == webrtc.PeerConnectionStateFailed ||
			st == webrtc.PeerConnectionStateDisconnected ||
			st == webrtc.PeerConnectionStateClosed {
			cancel()
			s.closeSession(id)
		}
	})

	go func() {
		defer cancel()
		if waitErr := client.Wait(); waitErr != nil && !isExpectedCloseError(waitErr) {
			logrus.Warnf("pion-bridge[%s]: rtsp ended: %v", key, waitErr)
		}
		s.closeSession(id)
	}()

	return id, pc.LocalDescription().SDP, nil
}

func (s *Service) CloseSession(id string) {
	s.closeSession(strings.TrimSpace(id))
}

func (s *Service) Close() {
	s.mu.Lock()
	ids := make([]string, 0, len(s.sessions))
	for id := range s.sessions {
		ids = append(ids, id)
	}
	s.mu.Unlock()

	for _, id := range ids {
		s.closeSession(id)
	}
}

func (s *Service) putSession(ss *session) {
	s.mu.Lock()
	s.sessions[ss.id] = ss
	s.mu.Unlock()
}

func (s *Service) closeSession(id string) {
	if id == "" {
		return
	}

	s.mu.Lock()
	ss, ok := s.sessions[id]
	if ok {
		delete(s.sessions, id)
	}
	s.mu.Unlock()
	if !ok || ss == nil {
		return
	}
	if !ss.closed.CompareAndSwap(false, true) {
		return
	}

	if ss.pc != nil {
		_ = ss.pc.Close()
	}
	if ss.rtsp != nil {
		ss.rtsp.Close()
	}

	logrus.Infof("pion-bridge[%s]: closed session=%s", ss.streamKey, ss.id)
}

func selectTracks(desc *description.Session) ([]selectedTrack, []*description.Media, error) {
	var tracks []selectedTrack
	var medias []*description.Media

	add := func(medi *description.Media, forma format.Format, cap webrtc.RTPCodecCapability, id string) error {
		t, err := webrtc.NewTrackLocalStaticRTP(cap, id, "go-cam")
		if err != nil {
			return err
		}
		tracks = append(tracks, selectedTrack{
			media: medi,
			forma: forma,
			track: t,
		})
		medias = append(medias, medi)
		return nil
	}

	var h264 *format.H264
	if medi := desc.FindFormat(&h264); medi != nil {
		if err := add(medi, h264, webrtc.RTPCodecCapability{
			MimeType:    webrtc.MimeTypeH264,
			ClockRate:   90000,
			SDPFmtpLine: "",
		}, "video"); err != nil {
			return nil, nil, err
		}
	}

	if len(tracks) == 0 {
		var vp8 *format.VP8
		if medi := desc.FindFormat(&vp8); medi != nil {
			if err := add(medi, vp8, webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeVP8,
				ClockRate: 90000,
			}, "video"); err != nil {
				return nil, nil, err
			}
		}
	}

	if len(tracks) == 0 {
		var vp9 *format.VP9
		if medi := desc.FindFormat(&vp9); medi != nil {
			if err := add(medi, vp9, webrtc.RTPCodecCapability{
				MimeType:  webrtc.MimeTypeVP9,
				ClockRate: 90000,
			}, "video"); err != nil {
				return nil, nil, err
			}
		}
	}

	var opus *format.Opus
	if medi := desc.FindFormat(&opus); medi != nil {
		if err := add(medi, opus, webrtc.RTPCodecCapability{
			MimeType:  webrtc.MimeTypeOpus,
			ClockRate: 48000,
			Channels:  2,
		}, "audio"); err != nil {
			return nil, nil, err
		}
	}

	if len(tracks) == 0 {
		return nil, nil, fmt.Errorf("no supported rtsp codecs found (need h264/vp8/vp9/opus)")
	}

	medias = uniqueMedias(medias)
	return tracks, medias, nil
}

func uniqueMedias(in []*description.Media) []*description.Media {
	if len(in) <= 1 {
		return in
	}
	out := make([]*description.Media, 0, len(in))
	seen := map[*description.Media]bool{}
	for _, m := range in {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

func drainRTCP(ctx context.Context, sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if _, _, err := sender.Read(buf); err != nil {
				return
			}
		}
	}
}

func isExpectedCloseError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "closed") ||
		strings.Contains(msg, "terminated") ||
		strings.Contains(msg, "eof") ||
		strings.Contains(msg, context.Canceled.Error()) ||
		strings.Contains(msg, http.ErrServerClosed.Error()) ||
		strings.Contains(msg, "use of closed network connection")
}

func (s *Service) SessionTTL() time.Duration {
	if s.cfg.SessionTTLSeconds <= 0 {
		return 120 * time.Second
	}
	return time.Duration(s.cfg.SessionTTLSeconds) * time.Second
}
