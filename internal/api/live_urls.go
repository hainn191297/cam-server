package api

import (
	"strings"
)

type resolvedLiveURLs struct {
	HLS         string
	WebRTC      string
	WebRTCOffer string
	RTSP        string
	RTMP        string
}

func (s *Server) resolveLiveURLs(streamKey string) resolvedLiveURLs {
	key := strings.Trim(strings.TrimSpace(streamKey), "/")
	out := resolvedLiveURLs{}

	if s.mtx != nil {
		urls := s.mtx.BuildLiveURLs(key)
		out.HLS = urls.HLS
		out.WebRTC = urls.WebRTC
		out.RTSP = urls.RTSP
		out.RTMP = urls.RTMP
	}

	if s.pion != nil {
		out.WebRTCOffer = s.pion.PublicOfferURL(key)
		// Prefer Pion signaling endpoint over MediaMTX built-in WebRTC URL.
		out.WebRTC = out.WebRTCOffer
	}

	if out.HLS == "" {
		out.HLS = s.defaultHLSURL(key)
	}
	return out
}

func (s *Server) defaultHLSURL(streamKey string) string {
	base := defaultHTTPBaseURL(s.cfg.HTTP.Addr)
	key := strings.Trim(strings.TrimSpace(streamKey), "/")
	return base + "/hls/" + key + "/index.m3u8"
}

func defaultHTTPBaseURL(addr string) string {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return "http://localhost:8080"
	}
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return strings.TrimRight(raw, "/")
	}
	if strings.HasPrefix(raw, ":") {
		return "http://localhost" + raw
	}
	return "http://" + strings.TrimRight(raw, "/")
}
