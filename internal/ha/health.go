package ha

import (
	"context"
	"sync"
	"time"
)

type HealthMode string

const (
	ModeNormal               HealthMode = "normal"
	ModeDegradedControlPlane HealthMode = "degraded_control_plane"
)

type DependencyStatus string

const (
	DependencyUp   DependencyStatus = "up"
	DependencyDown DependencyStatus = "down"
)

type Dependency struct {
	Name     string
	Required bool
	Check    func(context.Context) error
}

type DependencyResult struct {
	Name      string           `json:"name"`
	Required  bool             `json:"required"`
	Status    DependencyStatus `json:"status"`
	LatencyMs int64            `json:"latency_ms"`
	Error     string           `json:"error,omitempty"`
}

type Snapshot struct {
	Status       string             `json:"status"`
	Mode         HealthMode         `json:"mode"`
	CheckedAt    time.Time          `json:"checked_at"`
	Dependencies []DependencyResult `json:"dependencies"`
}

type Monitor struct {
	timeout      time.Duration
	dependencies []Dependency
	now          func() time.Time
}

func NewMonitor(timeout time.Duration, dependencies []Dependency) *Monitor {
	if timeout <= 0 {
		timeout = 1200 * time.Millisecond
	}
	return &Monitor{
		timeout:      timeout,
		dependencies: dependencies,
		now:          time.Now,
	}
}

func (m *Monitor) Snapshot(ctx context.Context) Snapshot {
	out := Snapshot{
		Status:    "healthy",
		Mode:      ModeNormal,
		CheckedAt: m.now().UTC(),
	}
	if len(m.dependencies) == 0 {
		return out
	}

	results := make([]DependencyResult, len(m.dependencies))
	var wg sync.WaitGroup
	wg.Add(len(m.dependencies))

	for i, dep := range m.dependencies {
		i := i
		dep := dep

		go func() {
			defer wg.Done()

			result := DependencyResult{
				Name:     dep.Name,
				Required: dep.Required,
				Status:   DependencyUp,
			}
			if dep.Check == nil {
				result.Status = DependencyDown
				result.Error = "dependency check function is nil"
				results[i] = result
				return
			}

			checkCtx, cancel := context.WithTimeout(ctx, m.timeout)
			start := m.now()
			err := dep.Check(checkCtx)
			cancel()

			result.LatencyMs = m.now().Sub(start).Milliseconds()
			if err != nil {
				result.Status = DependencyDown
				result.Error = err.Error()
			}
			results[i] = result
		}()
	}

	wg.Wait()

	for _, r := range results {
		if r.Required && r.Status == DependencyDown {
			out.Status = "degraded"
			out.Mode = ModeDegradedControlPlane
			break
		}
	}
	out.Dependencies = results
	return out
}
