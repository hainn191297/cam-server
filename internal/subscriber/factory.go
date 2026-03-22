package subscriber

import (
	"go-cam-server/config"
	vmsminio "go-cam-server/internal/minio"
	"go-cam-server/internal/stream"
	"go-cam-server/internal/tracectx"
)

// Factory creates subscriber instances.
// Centralizes construction so the rtmp package doesn't import subscriber internals.
type Factory struct {
	minioClient          *vmsminio.Client
	minioSegmentDuration int
}

func NewFactory(minioClient *vmsminio.Client, minioSegmentDuration int) *Factory {
	if minioSegmentDuration <= 0 {
		minioSegmentDuration = 10
	}
	return &Factory{
		minioClient:          minioClient,
		minioSegmentDuration: minioSegmentDuration,
	}
}

func (f *Factory) NewStorage(streamKey, rootPath string, tc tracectx.Context) *StorageSubscriber {
	return NewStorageSubscriber(streamKey, rootPath, tc)
}

func (f *Factory) NewHLS(streamKey, hlsRoot string, segmentDuration, maxSegments int, tc tracectx.Context) *HLSSubscriber {
	return NewHLSSubscriber(streamKey, hlsRoot, segmentDuration, maxSegments, tc)
}

func (f *Factory) NewMinIO(streamKey string, tc tracectx.Context) *MinIOSubscriber {
	if f == nil || f.minioClient == nil {
		return nil
	}
	segmentDuration := f.minioSegmentDuration
	if segmentDuration <= 0 {
		segmentDuration = 10
	}
	return NewMinIOSubscriber(streamKey, f.minioClient, segmentDuration, tc)
}

// PublishSubscribers are attached to local camera publishers (RTMP ingest).
func (f *Factory) PublishSubscribers(streamKey string, cfg *config.Config, tc tracectx.Context) []stream.Subscriber {
	subs := []stream.Subscriber{
		f.NewStorage(streamKey, cfg.Storage.RootPath, tc),
		f.NewHLS(streamKey, cfg.HLS.RootPath, cfg.HLS.SegmentDuration, cfg.HLS.MaxSegments, tc),
	}
	if minioSub := f.NewMinIO(streamKey, tc); minioSub != nil {
		subs = append(subs, minioSub)
	}
	return subs
}

// RelaySubscribers are attached to relayed streams pulled from another node.
// MinIO upload is intentionally skipped here to avoid duplicate cloud writes.
func (f *Factory) RelaySubscribers(streamKey string, cfg *config.Config, tc tracectx.Context) []stream.Subscriber {
	return []stream.Subscriber{
		f.NewStorage(streamKey, cfg.Storage.RootPath, tc),
		f.NewHLS(streamKey, cfg.HLS.RootPath, cfg.HLS.SegmentDuration, cfg.HLS.MaxSegments, tc),
	}
}
