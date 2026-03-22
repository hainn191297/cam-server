package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	RTMP         RTMPConfig         `yaml:"rtmp"`
	HTTP         HTTPConfig         `yaml:"http"`
	Redis        RedisConfig        `yaml:"redis"`
	Node         NodeConfig         `yaml:"node"`
	Topology     TopologyConfig     `yaml:"topology"`
	HA           HAConfig           `yaml:"ha"`
	SLO          SLOConfig          `yaml:"slo"`
	HLS          HLSConfig          `yaml:"hls"`
	Storage      StorageConfig      `yaml:"storage"`
	MinIO        MinIOConfig        `yaml:"minio"`
	MediaMTX     MediaMTXConfig     `yaml:"media_mtx"`
	MediaMTXProc MediaMTXProcConfig `yaml:"media_mtx_proc"`
	PionWebRTC   PionWebRTCConfig   `yaml:"pion_webrtc"`
	Logging      LoggingConfig      `yaml:"logging"`
}

type RTMPConfig struct {
	Enabled bool   `yaml:"enabled"`
	Addr    string `yaml:"addr"`
}

type HTTPConfig struct {
	Addr string `yaml:"addr"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

type NodeConfig struct {
	ID                string        `yaml:"id"`
	HTTPAddr          string        `yaml:"http_addr"`
	Role              string        `yaml:"role"`
	HeartbeatInterval time.Duration `yaml:"heartbeat_interval"`
}

type TopologyConfig struct {
	Region string `yaml:"region"`
	AZ     string `yaml:"az"`
}

type HAConfig struct {
	DependencyTimeoutMs   int    `yaml:"dependency_timeout_ms"`
	LeaseTTLSeconds       int    `yaml:"lease_ttl_seconds"`
	LeaseClockSkewSeconds int    `yaml:"lease_clock_skew_seconds"`
	LeaseTokenSecret      string `yaml:"lease_token_secret"`
	IdempotencyTTLSeconds int    `yaml:"idempotency_ttl_seconds"`
	IdempotencyMaxKeys    int    `yaml:"idempotency_max_keys"`
}

type SLOConfig struct {
	APIAvailabilityTarget    float64 `yaml:"api_availability_target"`
	StreamStartSuccessTarget float64 `yaml:"stream_start_success_target"`
	FirstFrameP95MsTarget    int     `yaml:"first_frame_p95_ms_target"`
	ReconnectSuccessTarget   float64 `yaml:"reconnect_success_target"`
}

type HLSConfig struct {
	RootPath        string `yaml:"root_path"`
	SegmentDuration int    `yaml:"segment_duration"` // seconds per segment
	MaxSegments     int    `yaml:"max_segments"`     // rolling playlist length
}

type StorageConfig struct {
	RootPath string `yaml:"root_path"` // local fallback (used alongside MinIO)
}

type MinIOConfig struct {
	Endpoint        string `yaml:"endpoint"`   // e.g. "localhost:9000"
	AccessKey       string `yaml:"access_key"` // MinIO root user
	SecretKey       string `yaml:"secret_key"` // MinIO root password
	Bucket          string `yaml:"bucket"`     // default "recordings"
	UseSSL          bool   `yaml:"use_ssl"`
	SegmentDuration int    `yaml:"segment_duration"` // seconds per recording segment
	PresignExpiry   int    `yaml:"presign_expiry"`   // presigned URL TTL in minutes
	Enabled         bool   `yaml:"enabled"`
}

type MediaMTXConfig struct {
	Enabled         bool   `yaml:"enabled"`           // switch live/playback URLs to MediaMTX
	APIBaseURL      string `yaml:"api_base_url"`      // e.g. "http://localhost:9997"
	HLSBaseURL      string `yaml:"hls_base_url"`      // e.g. "http://localhost:8888"
	WebRTCBaseURL   string `yaml:"webrtc_base_url"`   // e.g. "http://localhost:8889"
	RTSPBaseURL     string `yaml:"rtsp_base_url"`     // e.g. "rtsp://localhost:8554"
	RTMPBaseURL     string `yaml:"rtmp_base_url"`     // e.g. "rtmp://localhost:1935"
	PlaybackBaseURL string `yaml:"playback_base_url"` // e.g. "http://localhost:9996"
	PlaybackFormat  string `yaml:"playback_format"`   // mp4 or mpegts
}

type MediaMTXProcConfig struct {
	Enabled      bool   `yaml:"enabled"`       // start mediamtx as a child process
	BinaryPath   string `yaml:"binary_path"`   // path to mediamtx binary, e.g. "./mediamtx"
	ConfigPath   string `yaml:"config_path"`   // mediamtx.yml to pass as argument
	ReadyTimeout int    `yaml:"ready_timeout"` // seconds to wait for mediamtx API to respond
}

type PionWebRTCConfig struct {
	Enabled           bool     `yaml:"enabled"`              // enable Pion offer/answer API
	PublicBaseURL     string   `yaml:"public_base_url"`      // API base exposed to web/app, e.g. http://localhost:8080
	SourceRTSPBaseURL string   `yaml:"source_rtsp_base_url"` // source RTSP root, e.g. rtsp://localhost:8554
	ICEServers        []string `yaml:"ice_servers"`          // STUN/TURN URLs
	SessionTTLSeconds int      `yaml:"session_ttl_seconds"`  // auto-close guard for stale sessions
}

type LoggingConfig struct {
	Level           string          `yaml:"level"`             // info|debug|warn|error
	Dir             string          `yaml:"dir"`               // log directory
	InfoFile        string          `yaml:"info_file"`         // general logs
	ErrorFile       string          `yaml:"error_file"`        // warning/error logs
	StatFile        string          `yaml:"stat_file"`         // metrics/statistics
	SlowFile        string          `yaml:"slow_file"`         // slow operations
	SlowThresholdMs int             `yaml:"slow_threshold_ms"` // slow threshold in milliseconds
	StatIntervalSec int             `yaml:"stat_interval_sec"` // runtime stat emit interval
	Telemetry       TelemetryConfig `yaml:"telemetry"`
}

type TelemetryConfig struct {
	Enabled   bool   `yaml:"enabled"`
	Endpoint  string `yaml:"endpoint"`   // e.g. https://telemetry.example.com/logs
	TimeoutMs int    `yaml:"timeout_ms"` // per-request timeout
	QueueSize int    `yaml:"queue_size"` // async queue size
}

// Load reads config from path. Missing file → use defaults.
func Load(path string) (*Config, error) {
	cfg := defaults()

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	defer f.Close()

	return cfg, yaml.NewDecoder(f).Decode(cfg)
}

func defaults() *Config {
	return &Config{
		RTMP:  RTMPConfig{Enabled: true, Addr: ":1935"},
		HTTP:  HTTPConfig{Addr: ":8080"},
		Redis: RedisConfig{Addr: "localhost:6379"},
		Node: NodeConfig{
			ID:                "node-1",
			HTTPAddr:          "localhost:8080",
			Role:              "master",
			HeartbeatInterval: 5 * time.Second,
		},
		Topology: TopologyConfig{
			Region: "local-dev",
			AZ:     "az1",
		},
		HA: HAConfig{
			DependencyTimeoutMs:   1200,
			LeaseTTLSeconds:       90,
			LeaseClockSkewSeconds: 15,
			LeaseTokenSecret:      "dev-only-change-me",
			IdempotencyTTLSeconds: 180,
			IdempotencyMaxKeys:    20000,
		},
		SLO: SLOConfig{
			APIAvailabilityTarget:    99.95,
			StreamStartSuccessTarget: 99.9,
			FirstFrameP95MsTarget:    2500,
			ReconnectSuccessTarget:   99.9,
		},
		HLS: HLSConfig{
			RootPath:        "./data/hls",
			SegmentDuration: 2,
			MaxSegments:     5,
		},
		Storage: StorageConfig{
			RootPath: "./data/storage",
		},
		MinIO: MinIOConfig{
			Endpoint:        "localhost:9000",
			AccessKey:       "minioadmin",
			SecretKey:       "minioadmin",
			Bucket:          "recordings",
			UseSSL:          false,
			SegmentDuration: 10, // upload a new segment every 10s
			PresignExpiry:   60, // presigned URL valid for 60 min
			Enabled:         false,
		},
		MediaMTXProc: MediaMTXProcConfig{
			Enabled:      false,
			BinaryPath:   "./mediamtx",
			ConfigPath:   "./mediamtx.yml",
			ReadyTimeout: 15,
		},
		MediaMTX: MediaMTXConfig{
			Enabled:         false,
			APIBaseURL:      "http://localhost:9997",
			HLSBaseURL:      "http://localhost:8888",
			WebRTCBaseURL:   "http://localhost:8889",
			RTSPBaseURL:     "rtsp://localhost:8554",
			RTMPBaseURL:     "rtmp://localhost:1935",
			PlaybackBaseURL: "http://localhost:9996",
			PlaybackFormat:  "mp4",
		},
		PionWebRTC: PionWebRTCConfig{
			Enabled:           false,
			PublicBaseURL:     "http://localhost:8080",
			SourceRTSPBaseURL: "rtsp://localhost:8554",
			ICEServers:        []string{"stun:stun.l.google.com:19302"},
			SessionTTLSeconds: 120,
		},
		Logging: LoggingConfig{
			Level:           "info",
			Dir:             "./data/logs",
			InfoFile:        "info.log",
			ErrorFile:       "error.log",
			StatFile:        "stat.log",
			SlowFile:        "slow.log",
			SlowThresholdMs: 1000,
			StatIntervalSec: 30,
			Telemetry: TelemetryConfig{
				Enabled:   false,
				Endpoint:  "",
				TimeoutMs: 1500,
				QueueSize: 2048,
			},
		},
	}
}
