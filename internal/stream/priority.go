package stream

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// ScoringWeights controls how each factor contributes to a stream's priority.
// All weights should sum to ~1.0. Tunable via the API at runtime.
type ScoringWeights struct {
	ViewerCount    float64 `json:"viewer_count"`    // 0.4 — more live viewers = higher priority
	BitrateHealth  float64 `json:"bitrate_health"`  // 0.2 — stable bitrate = reliable stream
	Recency        float64 `json:"recency"`         // 0.1 — streams > 30s old score higher (warmup)
	ManualPin      float64 `json:"manual_pin"`      // 0.1 — operator-pinned cameras always surface
	MotionActivity float64 `json:"motion_activity"` // 0.2 — placeholder: hook into AI events
}

var DefaultWeights = ScoringWeights{
	ViewerCount:    0.4,
	BitrateHealth:  0.2,
	Recency:        0.1,
	ManualPin:      0.1,
	MotionActivity: 0.2,
}

// StreamScore is the computed priority for a single stream.
type StreamScore struct {
	StreamKey string    `json:"stream_key"`
	Score     float64   `json:"score"` // [0.0, 1.0]
	Viewers   int       `json:"viewers"`
	AgeSeconds float64  `json:"age_seconds"`
	Pinned    bool      `json:"pinned"`
	Reasons   []string  `json:"reasons"`
	ScoredAt  time.Time `json:"scored_at"`
}

// ScoreStream computes a normalized [0.0, 1.0] priority score for s.
//
// Used by the monitor API to fill display grid slots in priority order.
// Factor breakdown:
//
//	viewer_count    → how many TypeLivestream subs are currently attached
//	bitrate_health  → stream health (TODO: wire in real bitrate from PublisherStats)
//	recency         → penalty for streams < 30s old (still warming up)
//	manual_pin      → operator flagged this stream via POST /streams/:key/pin
//	motion_activity → TODO: hook into ai_events table via AIBoxEventService
func ScoreStream(s Stream, weights ScoringWeights, pinnedKeys map[string]bool) StreamScore {
	const (
		maxViewers        = 100.0
		warmupSeconds     = 30.0
		expectedBitrateOK = 0.75 // placeholder: assume 75% of streams are healthy
	)

	viewers := countLivestreamSubs(s)
	age := time.Since(s.Stats().StartedAt).Seconds()
	pinned := pinnedKeys[s.Key()]

	viewerScore := math.Min(float64(viewers)/maxViewers, 1.0) * weights.ViewerCount
	bitrateScore := expectedBitrateOK * weights.BitrateHealth // TODO: real bitrate
	recencyScore := math.Min(age/warmupSeconds, 1.0) * weights.Recency
	pinScore := 0.0
	if pinned {
		pinScore = weights.ManualPin
	}
	motionScore := 0.0 * weights.MotionActivity // TODO: AI events

	total := math.Min(viewerScore+bitrateScore+recencyScore+pinScore+motionScore, 1.0)

	return StreamScore{
		StreamKey:  s.Key(),
		Score:      total,
		Viewers:    viewers,
		AgeSeconds: age,
		Pinned:     pinned,
		Reasons: []string{
			fmt.Sprintf("viewer=%.3f(n=%d)", viewerScore, viewers),
			fmt.Sprintf("bitrate=%.3f", bitrateScore),
			fmt.Sprintf("recency=%.3f(%.0fs)", recencyScore, age),
			fmt.Sprintf("pin=%.3f", pinScore),
			fmt.Sprintf("motion=%.3f(todo)", motionScore),
		},
		ScoredAt: time.Now(),
	}
}

// RankStreams scores all streams and returns them sorted highest-first.
//
// The monitor API calls this to fill N display slots in priority order:
//
//	slots[0] = most important stream (most viewers / pinned / motion)
//	slots[N] = least important (new, no viewers, no motion)
func RankStreams(streams []Stream, weights ScoringWeights, pinnedKeys map[string]bool) []StreamScore {
	scores := make([]StreamScore, len(streams))
	for i, s := range streams {
		scores[i] = ScoreStream(s, weights, pinnedKeys)
	}
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})
	return scores
}

func countLivestreamSubs(s Stream) int {
	n := 0
	for _, sub := range s.Subscribers() {
		if sub.Type() == TypeLivestream {
			n++
		}
	}
	return n
}
