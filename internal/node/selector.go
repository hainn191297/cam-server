package node

import (
	"errors"
	"math"
	"time"
)

var ErrNoAvailableNode = errors.New("no available node in cluster")

// SelectNode picks the least-loaded live node from a list.
//
// Ports the C++ GetNodeAvaliableService.cpp logic:
//   - Skip stale nodes (last heartbeat > 10s ago)
//   - Filter by role if specified ("master" or "slave" or "" for any)
//   - Return the node with the fewest active streams (least-load)
func SelectNode(nodes []NodeInfo, preferRole string) (NodeInfo, error) {
	best := NodeInfo{}
	bestLoad := math.MaxInt32
	staleThreshold := 10 * time.Second
	now := time.Now()

	for _, n := range nodes {
		if now.Sub(n.UpdatedAt) > staleThreshold {
			continue // stale — same threshold as C++ KeepAliveService
		}
		if preferRole != "" && n.Role != preferRole {
			continue
		}
		if n.StreamCount < bestLoad {
			bestLoad = n.StreamCount
			best = n
		}
	}

	if bestLoad == math.MaxInt32 {
		return NodeInfo{}, ErrNoAvailableNode
	}
	return best, nil
}
