// Package minio wraps the MinIO Go client for the VMS recording pipeline.
//
// Responsibilities:
//   - Connect to MinIO / S3-compatible storage
//   - Ensure recording bucket exists on startup
//   - Upload FLV segments (PutObject)
//   - List recordings for a stream (ListObjects)
//   - Generate presigned download URLs (PresignedGetObject)
package minio

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/sirupsen/logrus"

	"go-cam-server/config"
)

// Client wraps the MinIO SDK client with VMS-specific helpers.
type Client struct {
	mc     *miniogo.Client
	bucket string
	cfg    config.MinIOConfig
}

// New creates a MinIO client and ensures the recording bucket exists.
// Returns nil (not error) when MinIO is disabled or unreachable —
// the server runs without cloud storage in that case.
func New(cfg config.MinIOConfig) *Client {
	if !cfg.Enabled {
		logrus.Info("minio: disabled — recordings stored locally only")
		return nil
	}

	mc, err := miniogo.New(cfg.Endpoint, &miniogo.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		logrus.Errorf("minio: init failed: %v", err)
		return nil
	}

	c := &Client{mc: mc, bucket: cfg.Bucket, cfg: cfg}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := c.ensureBucket(ctx); err != nil {
		logrus.Errorf("minio: ensure bucket %q: %v", cfg.Bucket, err)
		return nil
	}

	logrus.Infof("minio: connected to %s, bucket=%s", cfg.Endpoint, cfg.Bucket)
	return c
}

// UploadSegment uploads a FLV recording segment to MinIO.
//
// Object key: recordings/{streamKey}/{unixMs}.flv
// Caller provides the raw FLV bytes in data.
func (c *Client) UploadSegment(ctx context.Context, streamKey string, data []byte) (objectKey string, err error) {
	key := fmt.Sprintf("recordings/%s/%d.flv", streamKey, time.Now().UnixMilli())

	_, err = c.mc.PutObject(ctx, c.bucket, key,
		bytes.NewReader(data),
		int64(len(data)),
		miniogo.PutObjectOptions{ContentType: "video/x-flv"},
	)
	if err != nil {
		return "", fmt.Errorf("minio upload %s: %w", key, err)
	}

	logrus.Infof("minio: uploaded %s (%d bytes)", key, len(data))
	return key, nil
}

// Health probes object storage connectivity by checking bucket existence.
func (c *Client) Health(ctx context.Context) error {
	_, err := c.mc.BucketExists(ctx, c.bucket)
	return err
}

// RecordingInfo describes a single uploaded recording segment.
type RecordingInfo struct {
	ObjectKey    string    `json:"object_key"`
	StreamKey    string    `json:"stream_key"`
	Size         int64     `json:"size_bytes"`
	LastModified time.Time `json:"last_modified"`
	PresignedURL string    `json:"presigned_url"` // filled by ListRecordings
}

// ListRecordings returns all recorded segments for a stream, newest first.
// Each entry includes a presigned download URL valid for cfg.PresignExpiry minutes.
func (c *Client) ListRecordings(ctx context.Context, streamKey string) ([]RecordingInfo, error) {
	prefix := fmt.Sprintf("recordings/%s/", streamKey)
	expiry := time.Duration(c.cfg.PresignExpiry) * time.Minute

	var items []RecordingInfo
	for obj := range c.mc.ListObjects(ctx, c.bucket, miniogo.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}

		url, err := c.mc.PresignedGetObject(ctx, c.bucket, obj.Key, expiry, nil)
		if err != nil {
			logrus.Warnf("minio: presign %s: %v", obj.Key, err)
			continue
		}

		items = append(items, RecordingInfo{
			ObjectKey:    obj.Key,
			StreamKey:    streamKey,
			Size:         obj.Size,
			LastModified: obj.LastModified,
			PresignedURL: url.String(),
		})
	}

	// Sort newest first (stable regardless of backend object listing order).
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastModified.After(items[j].LastModified)
	})

	return items, nil
}

// AllStreamKeys returns the distinct stream keys that have recordings in the bucket.
func (c *Client) AllStreamKeys(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	prefix := "recordings/"

	for obj := range c.mc.ListObjects(ctx, c.bucket, miniogo.ListObjectsOptions{
		Prefix:    prefix,
		Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		// obj.Key looks like "recordings/cam1/1739999999.flv"
		parts := strings.Split(strings.TrimPrefix(obj.Key, prefix), "/")
		if len(parts) >= 2 && parts[0] != "" {
			seen[parts[0]] = true
		}
	}

	var keys []string
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}

// ─────────────────────────────────────────────────────────────────────────────

func (c *Client) ensureBucket(ctx context.Context) error {
	exists, err := c.mc.BucketExists(ctx, c.bucket)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return c.mc.MakeBucket(ctx, c.bucket, miniogo.MakeBucketOptions{})
}
