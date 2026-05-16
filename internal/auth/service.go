package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrInvalidCredentials = errors.New("invalid_credentials")
	ErrAccountDisabled    = errors.New("account_disabled")
	ErrTokenInvalid       = errors.New("token_invalid")
	ErrTokenExpired       = errors.New("token_expired")
)

type Service struct {
	store *Store
	jwt   *JWTService
}

func NewService(store *Store, jwt *JWTService) *Service {
	return &Service{store: store, jwt: jwt}
}

// SeedAdmin creates the default admin account if it doesn't exist.
// If password is empty, a random one is generated and printed.
func (s *Service) SeedAdmin(username, password string) (string, error) {
	if _, exists := s.store.GetByUsername(username); exists {
		return "", nil // already seeded
	}
	if password == "" {
		b := make([]byte, 12)
		rand.Read(b)
		password = hex.EncodeToString(b)
		logrus.Infof("auth.seed: generated admin password = %s (save this, it is shown once)", password)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash seed password: %w", err)
	}
	now := time.Now()
	u := &User{
		UserID:       uuid.New().String(),
		Username:     username,
		PasswordHash: string(hash),
		Role:         RoleAdmin,
		CreatedAt:    now,
		UpdatedAt:    now,
		IsActive:     true,
	}
	return password, s.store.AddUser(u)
}

// Login verifies credentials and issues a session token.
func (s *Service) Login(_ context.Context, req LoginRequest) (*LoginResponse, *SessionToken, error) {
	u, ok := s.store.GetByUsername(req.Username)
	// Always compare to prevent username enumeration timing.
	hash := "$2a$10$invalidhashfornonexistentuser000000000000000000000000"
	if ok {
		hash = u.PasswordHash
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		logrus.WithField("username_hash", hashField(req.Username)).Warn("auth.login: invalid credentials")
		return nil, nil, ErrInvalidCredentials
	}
	if !u.IsActive {
		return nil, nil, ErrAccountDisabled
	}

	sess, signed, err := s.jwt.Issue(u)
	if err != nil {
		return nil, nil, fmt.Errorf("issue token: %w", err)
	}
	s.store.SaveToken(sess)

	logrus.WithFields(logrus.Fields{
		"user_id":  u.UserID,
		"token_id": sess.TokenID,
		"role":     u.Role,
	}).Info("auth.login: success")

	return &LoginResponse{
		Token:     signed,
		ExpiresAt: sess.ExpiresAt,
		User:      UserInfo{UserID: u.UserID, Username: u.Username, Role: u.Role},
	}, sess, nil
}

// ValidateToken verifies the signed JWT and checks it hasn't been invalidated.
func (s *Service) ValidateToken(_ context.Context, signed string) (*SessionToken, *User, error) {
	claims, err := s.jwt.Verify(signed)
	if err != nil {
		return nil, nil, ErrTokenInvalid
	}

	tokenID := claims.ID
	sess, ok := s.store.GetToken(tokenID)
	if !ok {
		return nil, nil, ErrTokenInvalid
	}
	if sess.InvalidatedAt != nil {
		return nil, nil, ErrTokenInvalid
	}
	if time.Now().After(sess.ExpiresAt) {
		return nil, nil, ErrTokenExpired
	}

	u, ok := s.store.GetByID(sess.UserID)
	if !ok || !u.IsActive {
		return nil, nil, ErrTokenInvalid
	}
	return sess, u, nil
}

// Logout invalidates a token by ID.
func (s *Service) Logout(_ context.Context, tokenID string) {
	s.store.InvalidateToken(tokenID)
	logrus.WithField("token_id", tokenID).Info("auth.logout")
}

// GetSessionInfo returns session info for the FE.
func (s *Service) GetSessionInfo(sess *SessionToken, u *User) *SessionInfo {
	return &SessionInfo{
		UserID:      u.UserID,
		Username:    u.Username,
		Role:        u.Role,
		Permissions: u.Role.Permissions(),
		ExpiresAt:   sess.ExpiresAt,
	}
}

func hashField(s string) string {
	b := []byte(s)
	if len(b) > 3 {
		b[len(b)-1] = '*'
		b[len(b)-2] = '*'
	}
	return string(b)
}
