package ha

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const leaseKeyPrefix = "session_lease:"

var (
	ErrInvalidToken = errors.New("invalid reattach token")
	ErrLeaseExpired = errors.New("session lease expired")
	ErrLeaseMissing = errors.New("session lease not found")
)

type Lease struct {
	ID                  string    `json:"id"`
	StreamKey           string    `json:"stream_key"`
	TenantID            string    `json:"tenant_id"`
	DeviceID            string    `json:"device_id"`
	ViewerID            string    `json:"viewer_id"`
	PreferredProtocol   string    `json:"preferred_protocol"`
	LastKnownEndpoint   string    `json:"last_known_endpoint,omitempty"`
	HLSURL              string    `json:"hls_url"`
	WebRTCURL           string    `json:"webrtc_url,omitempty"`
	WebRTCOfferURL      string    `json:"webrtc_offer_url,omitempty"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	ControlPlaneBackend string    `json:"control_plane_backend"`
}

type LeaseInput struct {
	StreamKey         string
	TenantID          string
	DeviceID          string
	ViewerID          string
	PreferredProtocol string
	LastKnownEndpoint string
	HLSURL            string
	WebRTCURL         string
	WebRTCOfferURL    string
}

type tokenClaims struct {
	LeaseID   string `json:"lease_id"`
	StreamKey string `json:"stream_key"`
	ViewerID  string `json:"viewer_id"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

type leaseStore interface {
	Save(ctx context.Context, lease Lease, ttl time.Duration) error
	Get(ctx context.Context, leaseID string) (Lease, bool, error)
}

type LeaseService struct {
	store   leaseStore
	backend string
	ttl     time.Duration
	skew    time.Duration
	now     func() time.Time
	secret  []byte
}

func NewLeaseService(rdb *redis.Client, ttl, skew time.Duration, secret string) *LeaseService {
	if ttl <= 0 {
		ttl = 90 * time.Second
	}
	if skew < 0 {
		skew = 0
	}
	if secret == "" {
		secret = "dev-only-change-me"
	}

	svc := &LeaseService{
		ttl:    ttl,
		skew:   skew,
		now:    time.Now,
		secret: []byte(secret),
	}

	if rdb != nil {
		svc.store = &redisLeaseStore{rdb: rdb}
		svc.backend = "redis"
		return svc
	}

	svc.store = newMemoryLeaseStore()
	svc.backend = "memory"
	return svc
}

func (s *LeaseService) Backend() string {
	return s.backend
}

func (s *LeaseService) Issue(ctx context.Context, input LeaseInput) (Lease, string, error) {
	now := s.now().UTC()
	expiresAt := now.Add(s.ttl)

	lease := Lease{
		ID:                  uuid.NewString(),
		StreamKey:           strings.TrimSpace(input.StreamKey),
		TenantID:            strings.TrimSpace(input.TenantID),
		DeviceID:            strings.TrimSpace(input.DeviceID),
		ViewerID:            strings.TrimSpace(input.ViewerID),
		PreferredProtocol:   strings.TrimSpace(input.PreferredProtocol),
		LastKnownEndpoint:   strings.TrimSpace(input.LastKnownEndpoint),
		HLSURL:              strings.TrimSpace(input.HLSURL),
		WebRTCURL:           strings.TrimSpace(input.WebRTCURL),
		WebRTCOfferURL:      strings.TrimSpace(input.WebRTCOfferURL),
		IssuedAt:            now,
		ExpiresAt:           expiresAt,
		ControlPlaneBackend: s.backend,
	}
	if lease.PreferredProtocol == "" {
		lease.PreferredProtocol = "hls"
	}

	if err := s.store.Save(ctx, lease, s.ttl); err != nil {
		return Lease{}, "", err
	}

	token, err := s.signToken(tokenClaims{
		LeaseID:   lease.ID,
		StreamKey: lease.StreamKey,
		ViewerID:  lease.ViewerID,
		IssuedAt:  lease.IssuedAt.Unix(),
		ExpiresAt: lease.ExpiresAt.Unix(),
	})
	if err != nil {
		return Lease{}, "", err
	}

	return lease, token, nil
}

func (s *LeaseService) Reattach(ctx context.Context, token string) (Lease, string, error) {
	claims, err := s.parseToken(token)
	if err != nil {
		return Lease{}, "", err
	}

	now := s.now().UTC()
	issuedAt := time.Unix(claims.IssuedAt, 0).UTC()
	expiresAt := time.Unix(claims.ExpiresAt, 0).UTC()

	// Accept minor clock drift in distributed deployments.
	if now.Before(issuedAt.Add(-s.skew)) || now.After(expiresAt.Add(s.skew)) {
		return Lease{}, "", ErrLeaseExpired
	}

	lease, ok, err := s.store.Get(ctx, claims.LeaseID)
	if err != nil {
		return Lease{}, "", err
	}
	if !ok {
		return Lease{}, "", ErrLeaseMissing
	}
	if lease.StreamKey != claims.StreamKey || lease.ViewerID != claims.ViewerID {
		return Lease{}, "", ErrInvalidToken
	}
	if now.After(lease.ExpiresAt.Add(s.skew)) {
		return Lease{}, "", ErrLeaseExpired
	}

	lease.IssuedAt = now
	lease.ExpiresAt = now.Add(s.ttl)
	if err := s.store.Save(ctx, lease, s.ttl); err != nil {
		return Lease{}, "", err
	}

	newToken, err := s.signToken(tokenClaims{
		LeaseID:   lease.ID,
		StreamKey: lease.StreamKey,
		ViewerID:  lease.ViewerID,
		IssuedAt:  lease.IssuedAt.Unix(),
		ExpiresAt: lease.ExpiresAt.Unix(),
	})
	if err != nil {
		return Lease{}, "", err
	}

	return lease, newToken, nil
}

func (s *LeaseService) signToken(claims tokenClaims) (string, error) {
	payloadBytes, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(payloadBytes)
	signature := s.signature(payload)
	return payload + "." + signature, nil
}

func (s *LeaseService) parseToken(raw string) (tokenClaims, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return tokenClaims{}, ErrInvalidToken
	}
	payload := parts[0]
	signature := parts[1]
	expected := s.signature(payload)
	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return tokenClaims{}, ErrInvalidToken
	}

	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return tokenClaims{}, ErrInvalidToken
	}

	var claims tokenClaims
	if err := json.Unmarshal(data, &claims); err != nil {
		return tokenClaims{}, ErrInvalidToken
	}
	if claims.LeaseID == "" || claims.StreamKey == "" {
		return tokenClaims{}, ErrInvalidToken
	}
	return claims, nil
}

func (s *LeaseService) signature(payload string) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

type redisLeaseStore struct {
	rdb *redis.Client
}

func (s *redisLeaseStore) Save(ctx context.Context, lease Lease, ttl time.Duration) error {
	raw, err := json.Marshal(lease)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, leaseKeyPrefix+lease.ID, raw, ttl).Err()
}

func (s *redisLeaseStore) Get(ctx context.Context, leaseID string) (Lease, bool, error) {
	raw, err := s.rdb.Get(ctx, leaseKeyPrefix+leaseID).Result()
	if err == redis.Nil {
		return Lease{}, false, nil
	}
	if err != nil {
		return Lease{}, false, err
	}

	var lease Lease
	if err := json.Unmarshal([]byte(raw), &lease); err != nil {
		return Lease{}, false, err
	}
	return lease, true, nil
}

type memoryLeaseStore struct {
	mu    sync.RWMutex
	items map[string]Lease
	now   func() time.Time
}

func newMemoryLeaseStore() *memoryLeaseStore {
	return &memoryLeaseStore{
		items: make(map[string]Lease),
		now:   time.Now,
	}
}

func (s *memoryLeaseStore) Save(_ context.Context, lease Lease, _ time.Duration) error {
	s.mu.Lock()
	s.items[lease.ID] = lease
	s.gcLocked()
	s.mu.Unlock()
	return nil
}

func (s *memoryLeaseStore) Get(_ context.Context, leaseID string) (Lease, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.gcLocked()
	lease, ok := s.items[leaseID]
	return lease, ok, nil
}

func (s *memoryLeaseStore) gcLocked() {
	now := s.now().UTC()
	for k, lease := range s.items {
		if now.After(lease.ExpiresAt) {
			delete(s.items, k)
		}
	}
}
