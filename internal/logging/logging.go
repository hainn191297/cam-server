package logging

import (
	"bytes"
	"encoding/json"
	"io"
	stdlog "log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"

	"go-cam-server/config"
)

var slowThresholdMs atomic.Int64

type splitFileHook struct {
	formatter logrus.Formatter

	info  *os.File
	error *os.File
	stat  *os.File
	slow  *os.File

	mu sync.Mutex
}

func (h *splitFileHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *splitFileHook) Fire(entry *logrus.Entry) error {
	line, err := h.formatter.Format(entry)
	if err != nil {
		return err
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if channel, ok := entry.Data["channel"].(string); ok {
		switch channel {
		case "stat":
			_, err = h.stat.Write(line)
			return err
		case "slow":
			_, err = h.slow.Write(line)
			return err
		}
	}

	if entry.Level <= logrus.WarnLevel {
		if _, err = h.error.Write(line); err != nil {
			return err
		}
	}
	if entry.Level >= logrus.InfoLevel {
		_, err = h.info.Write(line)
		return err
	}

	return nil
}

type telemetryHook struct {
	client   *http.Client
	endpoint string
	queue    chan []byte
	stop     chan struct{}
	done     chan struct{}
}

func newTelemetryHook(cfg config.TelemetryConfig) *telemetryHook {
	if !cfg.Enabled || cfg.Endpoint == "" {
		return nil
	}

	timeoutMs := cfg.TimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = 1500
	}

	queueSize := cfg.QueueSize
	if queueSize <= 0 {
		queueSize = 2048
	}

	h := &telemetryHook{
		client: &http.Client{
			Timeout: time.Duration(timeoutMs) * time.Millisecond,
		},
		endpoint: cfg.Endpoint,
		queue:    make(chan []byte, queueSize),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	go h.worker()
	return h
}

func (h *telemetryHook) Levels() []logrus.Level { return logrus.AllLevels }

func (h *telemetryHook) Fire(entry *logrus.Entry) error {
	payload, err := json.Marshal(map[string]any{
		"timestamp": entry.Time.UTC().Format(time.RFC3339Nano),
		"level":     entry.Level.String(),
		"message":   entry.Message,
		"fields":    entry.Data,
	})
	if err != nil {
		return nil
	}

	select {
	case h.queue <- payload:
	default:
		// Backpressure policy: drop telemetry events when queue is full.
	}
	return nil
}

func (h *telemetryHook) Close() {
	select {
	case <-h.stop:
		return
	default:
		close(h.stop)
	}
	<-h.done
}

func (h *telemetryHook) worker() {
	defer close(h.done)

	for {
		select {
		case payload := <-h.queue:
			h.send(payload)
		case <-h.stop:
			// Best-effort drain on shutdown.
			for {
				select {
				case payload := <-h.queue:
					h.send(payload)
				default:
					return
				}
			}
		}
	}
}

func (h *telemetryHook) send(payload []byte) {
	req, err := http.NewRequest(http.MethodPost, h.endpoint, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

// Setup configures logrus to split all logs into dedicated files:
//   - info.log  (info/debug/trace)
//   - error.log (warn/error/fatal/panic)
//   - stat.log  (entries tagged with channel=stat)
//   - slow.log  (entries tagged with channel=slow)
//
// When telemetry is enabled, every log entry is also shipped asynchronously.
func Setup(cfg config.LoggingConfig) (cleanup func(), err error) {
	dir := cfg.Dir
	if dir == "" {
		dir = "./logs"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	infoName := cfg.InfoFile
	if infoName == "" {
		infoName = "info.log"
	}
	errorName := cfg.ErrorFile
	if errorName == "" {
		errorName = "error.log"
	}
	statName := cfg.StatFile
	if statName == "" {
		statName = "stat.log"
	}
	slowName := cfg.SlowFile
	if slowName == "" {
		slowName = "slow.log"
	}

	infoFile, err := openLogFile(filepath.Join(dir, infoName))
	if err != nil {
		return nil, err
	}
	errorFile, err := openLogFile(filepath.Join(dir, errorName))
	if err != nil {
		_ = infoFile.Close()
		return nil, err
	}
	statFile, err := openLogFile(filepath.Join(dir, statName))
	if err != nil {
		_ = infoFile.Close()
		_ = errorFile.Close()
		return nil, err
	}
	slowFile, err := openLogFile(filepath.Join(dir, slowName))
	if err != nil {
		_ = infoFile.Close()
		_ = errorFile.Close()
		_ = statFile.Close()
		return nil, err
	}

	threshold := cfg.SlowThresholdMs
	if threshold <= 0 {
		threshold = 1000
	}
	slowThresholdMs.Store(int64(threshold))

	level, err := parseLevel(cfg.Level)
	if err != nil {
		level = logrus.InfoLevel
	}

	formatter := &logrus.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
	}

	logger := logrus.StandardLogger()
	logger.SetOutput(io.Discard)
	logger.SetFormatter(formatter)
	logger.SetLevel(level)
	logger.ReplaceHooks(make(logrus.LevelHooks))

	fileHook := &splitFileHook{
		formatter: formatter,
		info:      infoFile,
		error:     errorFile,
		stat:      statFile,
		slow:      slowFile,
	}
	logger.AddHook(fileHook)

	tHook := newTelemetryHook(cfg.Telemetry)
	if tHook != nil {
		logger.AddHook(tHook)
	}

	stdWriter := logger.WriterLevel(logrus.InfoLevel)
	stdlog.SetFlags(0)
	stdlog.SetOutput(stdWriter)

	return func() {
		if tHook != nil {
			tHook.Close()
		}
		_ = stdWriter.Close()
		_ = slowFile.Close()
		_ = statFile.Close()
		_ = errorFile.Close()
		_ = infoFile.Close()
	}, nil
}

func Stat(event string, fields logrus.Fields) {
	entry := logrus.WithField("channel", "stat")
	if len(fields) > 0 {
		entry = entry.WithFields(fields)
	}
	entry.Info(event)
}

func Slow(event string, fields logrus.Fields) {
	entry := logrus.WithField("channel", "slow")
	if len(fields) > 0 {
		entry = entry.WithFields(fields)
	}
	entry.Warn(event)
}

func SlowIfExceeds(event string, elapsed time.Duration, fields logrus.Fields) {
	if elapsed < SlowThreshold() {
		return
	}
	out := make(logrus.Fields, len(fields)+1)
	for k, v := range fields {
		out[k] = v
	}
	out["elapsed_ms"] = elapsed.Milliseconds()
	Slow(event, out)
}

func SlowThreshold() time.Duration {
	ms := slowThresholdMs.Load()
	if ms <= 0 {
		ms = 1000
	}
	return time.Duration(ms) * time.Millisecond
}

func parseLevel(raw string) (logrus.Level, error) {
	if raw == "" {
		return logrus.InfoLevel, nil
	}
	return logrus.ParseLevel(raw)
}

func openLogFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
}
