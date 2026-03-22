// Package node handles cluster discovery and stream location.
//
// Each server instance registers itself in Redis with a TTL heartbeat.
// When a viewer requests a stream that lives on a different node,
// the node package provides the gRPC address of that node so the caller
// can relay or redirect.
//
// Redis key schema:
//
//	node:{nodeID}         HASH  → NodeInfo fields      TTL = 15s (refreshed every 5s)
//	stream:{streamKey}    STRING → nodeID               TTL = 30s
//
// Mirrors the C++ ServerData._nodes map + AppNode struct, but shared across
// nodes via Redis instead of in-memory (which only works on a single instance).
package node

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"go-cam-server/config"
)

const (
	nodeTTL          = 15 * time.Second
	streamTTL        = 30 * time.Second
	nodeKeyPattern   = "node:%s"
	nodeScanPattern  = "node:*"
	streamKeyPattern = "stream:%s"
)

// NodeInfo describes a single VMS node in the cluster.
type NodeInfo struct {
	ID          string    `json:"id"`
	HTTPAddr    string    `json:"http_addr"`
	Role        string    `json:"role"` // "master" or "slave"
	StreamCount int       `json:"stream_count"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Registry manages this node's presence in Redis and exposes cluster queries.
type Registry struct {
	rdb  *redis.Client
	self NodeInfo
	stop chan struct{}
}

func NewRegistry(rdb *redis.Client, cfg *config.Config) *Registry {
	return &Registry{
		rdb: rdb,
		self: NodeInfo{
			ID:       cfg.Node.ID,
			HTTPAddr: cfg.Node.HTTPAddr,
			Role:     cfg.Node.Role,
		},
		stop: make(chan struct{}),
	}
}

// Start launches the background heartbeat goroutine.
func (r *Registry) Start(streamCount func() int) {
	go r.heartbeat(streamCount)
}

// Stop cancels the heartbeat goroutine.
func (r *Registry) Stop() {
	select {
	case <-r.stop:
	default:
		close(r.stop)
	}
}

// AnnounceStream writes stream:{key} → nodeID into Redis.
// Called by the RTMP handler when a camera publishes.
func (r *Registry) AnnounceStream(ctx context.Context, streamKey string) error {
	key := fmt.Sprintf(streamKeyPattern, streamKey)
	return r.rdb.Set(ctx, key, r.self.ID, streamTTL).Err()
}

// RevokeStream deletes stream:{key} from Redis.
// Called by the RTMP handler OnClose.
func (r *Registry) RevokeStream(ctx context.Context, streamKey string) error {
	key := fmt.Sprintf(streamKeyPattern, streamKey)
	return r.rdb.Del(ctx, key).Err()
}

// FindStream returns the node ID that owns the given stream.
// Returns ("", ErrStreamNotFound) if the stream is not live anywhere.
func (r *Registry) FindStream(ctx context.Context, streamKey string) (string, error) {
	key := fmt.Sprintf(streamKeyPattern, streamKey)
	nodeID, err := r.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", fmt.Errorf("stream %q not found in cluster", streamKey)
	}
	return nodeID, err
}

// AllNodes returns all live nodes currently registered in Redis.
func (r *Registry) AllNodes(ctx context.Context) ([]NodeInfo, error) {
	keys, err := r.rdb.Keys(ctx, nodeScanPattern).Result()
	if err != nil {
		return nil, err
	}

	var nodes []NodeInfo
	for _, k := range keys {
		data, err := r.rdb.Get(ctx, k).Result()
		if err != nil {
			continue
		}
		var n NodeInfo
		if err := json.Unmarshal([]byte(data), &n); err != nil {
			continue
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// SelfID returns this node's ID.
func (r *Registry) SelfID() string { return r.self.ID }

// ─────────────────────────────────────────────────────────────────────────────

func (r *Registry) heartbeat(streamCount func() int) {
	ctx := context.Background()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	publish := func() {
		r.self.StreamCount = streamCount()
		r.self.UpdatedAt = time.Now()

		data, _ := json.Marshal(r.self)
		key := fmt.Sprintf(nodeKeyPattern, r.self.ID)
		if err := r.rdb.Set(ctx, key, data, nodeTTL).Err(); err != nil {
			logrus.Warnf("node-registry: heartbeat failed: %v", err)
		}
	}

	publish() // immediate registration on startup
	for {
		select {
		case <-ticker.C:
			publish()
		case <-r.stop:
			// Deregister immediately on shutdown
			key := fmt.Sprintf(nodeKeyPattern, r.self.ID)
			_ = r.rdb.Del(ctx, key)
			logrus.Infof("node-registry: deregistered %s", r.self.ID)
			return
		}
	}
}
