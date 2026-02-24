// Package mediamtxproc manages a MediaMTX child process lifecycle.
//
// Instead of running MediaMTX as a separate Docker container, this package
// starts the MediaMTX binary as a supervised subprocess. The Go server owns
// the process: it starts it, restarts it on crash, and terminates it cleanly
// on shutdown.
//
// Flow:
//
//	main() ──► Manager.Start(ctx)
//	               │  exec mediamtx binary
//	               │  pipe stdout/stderr → logrus
//	               │  health-check loop until API responds
//	               │  auto-restart on exit (exponential backoff)
//	               └─ cancel ctx ──► SIGTERM ──► wait ──► done
package mediamtxproc

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

const (
	defaultReadyTimeout = 15 * time.Second
	defaultReadyPoll    = 300 * time.Millisecond
	minRestartDelay     = 1 * time.Second
	maxRestartDelay     = 30 * time.Second
)

// Config holds the settings for the managed MediaMTX process.
type Config struct {
	// Enabled controls whether the Manager starts MediaMTX at all.
	Enabled bool

	// BinaryPath is the absolute (or PATH-relative) path to the mediamtx binary.
	BinaryPath string

	// ConfigPath is the mediamtx.yml config file passed as the first argument.
	ConfigPath string

	// APIBaseURL is used to health-check MediaMTX readiness, e.g. "http://localhost:9997".
	APIBaseURL string

	// ReadyTimeout is how long Manager.Start waits for MediaMTX to become
	// reachable before returning an error. Zero → defaultReadyTimeout (15s).
	ReadyTimeout time.Duration
}

// Manager supervises a single MediaMTX child process.
type Manager struct {
	cfg Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool
}

// New creates a Manager. Call Start to launch the process.
func New(cfg Config) *Manager {
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = defaultReadyTimeout
	}
	return &Manager{cfg: cfg}
}

// Start launches MediaMTX and blocks until it is reachable (or ctx is done).
// After readiness, it supervises the process in the background, restarting on
// unexpected exits until ctx is cancelled.
//
// Returns an error if the binary cannot be started or readiness times out.
func (m *Manager) Start(ctx context.Context) error {
	if err := m.launch(ctx); err != nil {
		return err
	}

	if err := m.waitReady(ctx); err != nil {
		m.terminate()
		return fmt.Errorf("mediamtx not ready: %w", err)
	}

	logrus.Info("mediamtxproc: ready")
	go m.supervise(ctx)
	return nil
}

// launch starts the mediamtx binary and pipes its output to logrus.
func (m *Manager) launch(ctx context.Context) error {
	args := []string{}
	if m.cfg.ConfigPath != "" {
		args = append(args, m.cfg.ConfigPath)
	}

	cmd := exec.CommandContext(ctx, m.cfg.BinaryPath, args...)
	cmd.Env = os.Environ()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mediamtxproc: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("mediamtxproc: stderr pipe: %w", err)
	}

	if err = cmd.Start(); err != nil {
		return fmt.Errorf("mediamtxproc: start %q: %w", m.cfg.BinaryPath, err)
	}

	m.mu.Lock()
	m.cmd = cmd
	m.mu.Unlock()

	logrus.Infof("mediamtxproc: started (pid=%d)", cmd.Process.Pid)

	go pipeLog(stdout, "mediamtx")
	go pipeLog(stderr, "mediamtx")

	return nil
}

// supervise watches the process and restarts it on unexpected exit.
func (m *Manager) supervise(ctx context.Context) {
	delay := minRestartDelay

	for {
		m.mu.Lock()
		cmd := m.cmd
		m.mu.Unlock()

		if cmd == nil {
			return
		}

		err := cmd.Wait()

		m.mu.Lock()
		stopped := m.stopped
		m.mu.Unlock()

		if stopped {
			return
		}

		if err != nil {
			logrus.Warnf("mediamtxproc: exited unexpectedly: %v — restarting in %s", err, delay)
		} else {
			logrus.Warnf("mediamtxproc: exited cleanly — restarting in %s", delay)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		// Exponential backoff.
		delay *= 2
		if delay > maxRestartDelay {
			delay = maxRestartDelay
		}

		if err := m.launch(ctx); err != nil {
			logrus.Errorf("mediamtxproc: restart failed: %v", err)
			continue
		}

		if err := m.waitReady(ctx); err != nil {
			logrus.Errorf("mediamtxproc: not ready after restart: %v", err)
			continue
		}

		logrus.Info("mediamtxproc: restarted and ready")
		delay = minRestartDelay // reset on successful restart
	}
}

// Stop sends SIGTERM to MediaMTX and waits for it to exit.
// It is safe to call Stop multiple times.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.stopped = true
	cmd := m.cmd
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	logrus.Info("mediamtxproc: stopping")
	m.terminate()
}

// terminate sends SIGTERM and waits briefly for graceful exit, then SIGKILL.
func (m *Manager) terminate() {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(os.Interrupt) // SIGINT → MediaMTX graceful shutdown

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		logrus.Info("mediamtxproc: stopped")
	case <-time.After(8 * time.Second):
		logrus.Warn("mediamtxproc: kill (timeout)")
		_ = cmd.Process.Kill()
	}
}

// waitReady polls the MediaMTX API until it responds or the timeout expires.
func (m *Manager) waitReady(ctx context.Context) error {
	healthURL := m.cfg.APIBaseURL + "/v3/config/global/get"
	deadline := time.Now().Add(m.cfg.ReadyTimeout)
	client := &http.Client{Timeout: 1 * time.Second}

	for {
		if time.Now().After(deadline) {
			return fmt.Errorf("timeout after %s", m.cfg.ReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		resp, err := client.Get(healthURL)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
		}

		time.Sleep(defaultReadyPoll)
	}
}

// pipeLog reads lines from r and forwards them to logrus.
func pipeLog(r interface{ Read([]byte) (int, error) }, prefix string) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		logrus.Infof("[%s] %s", prefix, scanner.Text())
	}
}
