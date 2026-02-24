// Package redis provides a thin Redis client wrapper.
package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"

	"go-cam-server/config"
)

// NewClient creates a Redis client from config and verifies connectivity.
// Returns nil (not an error) if Redis is unreachable — the server runs in
// single-node mode without cluster features.
func NewClient(cfg *config.Config) *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		logrus.Warnf("redis: unreachable (%s) — running single-node mode", cfg.Redis.Addr)
		_ = rdb.Close()
		return nil
	}

	logrus.Infof("redis: connected to %s", cfg.Redis.Addr)
	return rdb
}
