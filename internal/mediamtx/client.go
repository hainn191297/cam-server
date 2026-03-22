package mediamtx

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"go-cam-server/config"
)

type Client struct {
	cfg  config.MediaMTXConfig
	http *http.Client
}

func New(cfg config.MediaMTXConfig) *Client {
	if !cfg.Enabled {
		return nil
	}

	c := &Client{
		cfg: cfg,
		http: &http.Client{
			Timeout: 3 * time.Second,
		},
	}

	c.cfg.APIBaseURL = trimURL(cfg.APIBaseURL)
	c.cfg.HLSBaseURL = trimURL(cfg.HLSBaseURL)
	c.cfg.WebRTCBaseURL = trimURL(cfg.WebRTCBaseURL)
	c.cfg.RTSPBaseURL = trimURL(cfg.RTSPBaseURL)
	c.cfg.RTMPBaseURL = trimURL(cfg.RTMPBaseURL)
	c.cfg.PlaybackBaseURL = trimURL(cfg.PlaybackBaseURL)

	if c.cfg.PlaybackFormat == "" {
		c.cfg.PlaybackFormat = "mp4"
	}
	return c
}

type PathInfo struct {
	Name          string `json:"name"`
	Ready         bool   `json:"ready"`
	Source        string `json:"source,omitempty"`
	BytesReceived uint64 `json:"bytes_received,omitempty"`
}

type LiveURLs struct {
	StreamKey string `json:"stream_key"`
	HLS       string `json:"hls"`
	WebRTC    string `json:"webrtc"`
	RTSP      string `json:"rtsp"`
	RTMP      string `json:"rtmp"`
}

type PlaybackSegment struct {
	Start    string  `json:"start"`
	Duration float64 `json:"duration"`
	URL      string  `json:"url"`
}

type Summary struct {
	PathCount      int        `json:"path_count"`
	ReadyPathCount int        `json:"ready_path_count"`
	BytesReceived  uint64     `json:"bytes_received"`
	Paths          []PathInfo `json:"paths"`
}

func (c *Client) BuildLiveURLs(streamKey string) LiveURLs {
	key := normalizePath(streamKey)
	return LiveURLs{
		StreamKey: key,
		HLS:       joinPath(c.cfg.HLSBaseURL, key, "index.m3u8"),
		WebRTC:    joinPath(c.cfg.WebRTCBaseURL, key),
		RTSP:      joinPath(c.cfg.RTSPBaseURL, key),
		RTMP:      joinPath(c.cfg.RTMPBaseURL, key),
	}
}

func (c *Client) ListPaths(ctx context.Context) ([]PathInfo, error) {
	if c.cfg.APIBaseURL == "" {
		return nil, fmt.Errorf("mediamtx api_base_url is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+"/v3/paths/list", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mediamtx paths list: status=%d body=%s", resp.StatusCode, string(body))
	}

	var raw struct {
		Items []struct {
			Name          string `json:"name"`
			Ready         bool   `json:"ready"`
			Source        string `json:"source"`
			BytesReceived uint64 `json:"bytesReceived"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	out := make([]PathInfo, 0, len(raw.Items))
	for _, item := range raw.Items {
		out = append(out, PathInfo{
			Name:          item.Name,
			Ready:         item.Ready,
			Source:        item.Source,
			BytesReceived: item.BytesReceived,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Health performs a lightweight dependency probe for MediaMTX.
func (c *Client) Health(ctx context.Context) error {
	return c.Ping(ctx)
}

// Ping performs a lightweight dependency probe for MediaMTX.
func (c *Client) Ping(ctx context.Context) error {
	if c.cfg.APIBaseURL == "" {
		return fmt.Errorf("mediamtx api_base_url is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+"/v3/config/global/get", nil)
	if err != nil {
		return err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mediamtx ping: status=%d body=%s", resp.StatusCode, string(body))
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// Summary returns a detailed snapshot that control-plane endpoints can expose
// on demand without using it for every liveness probe.
func (c *Client) Summary(ctx context.Context) (Summary, error) {
	paths, err := c.ListPaths(ctx)
	if err != nil {
		return Summary{}, err
	}

	out := Summary{
		PathCount: len(paths),
		Paths:     paths,
	}
	for _, p := range paths {
		if p.Ready {
			out.ReadyPathCount++
		}
		out.BytesReceived += p.BytesReceived
	}
	return out, nil
}

func (c *Client) ListPlaybackSegments(ctx context.Context, streamKey string) ([]PlaybackSegment, error) {
	if c.cfg.PlaybackBaseURL == "" {
		return nil, fmt.Errorf("mediamtx playback_base_url is empty")
	}

	key := normalizePath(streamKey)
	q := url.Values{}
	q.Set("path", key)
	endpoint := c.cfg.PlaybackBaseURL + "/list?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("mediamtx playback list: status=%d body=%s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var direct []PlaybackSegment
	if err := json.Unmarshal(body, &direct); err == nil {
		return c.fillPlaybackURLs(key, direct), nil
	}

	var wrapped struct {
		Items []PlaybackSegment `json:"items"`
	}
	if err := json.Unmarshal(body, &wrapped); err == nil {
		return c.fillPlaybackURLs(key, wrapped.Items), nil
	}

	return nil, fmt.Errorf("mediamtx playback list: unexpected response body")
}

func (c *Client) fillPlaybackURLs(streamKey string, segments []PlaybackSegment) []PlaybackSegment {
	for i := range segments {
		if segments[i].URL == "" {
			segments[i].URL = c.BuildPlaybackGetURL(streamKey, segments[i].Start, segments[i].Duration)
		}
	}
	return segments
}

func (c *Client) BuildPlaybackGetURL(streamKey, start string, duration float64) string {
	if c.cfg.PlaybackBaseURL == "" {
		return ""
	}
	q := url.Values{}
	q.Set("path", normalizePath(streamKey))
	q.Set("start", start)
	q.Set("duration", fmt.Sprintf("%g", duration))
	q.Set("format", c.cfg.PlaybackFormat)
	return c.cfg.PlaybackBaseURL + "/get?" + q.Encode()
}

func trimURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func normalizePath(path string) string {
	return strings.Trim(strings.TrimSpace(path), "/")
}

func joinPath(base string, elems ...string) string {
	if base == "" {
		return ""
	}

	out := strings.TrimRight(base, "/")
	for _, e := range elems {
		e = normalizePath(e)
		if e == "" {
			continue
		}
		for _, part := range strings.Split(e, "/") {
			if part == "" {
				continue
			}
			out += "/" + url.PathEscape(part)
		}
	}
	return out
}
