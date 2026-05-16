package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type JWTService struct {
	privateKey *rsa.PrivateKey
	ttl        time.Duration
}

// NewJWTService creates a JWTService that signs tokens with RS256.
// privateKey must be a 2048-bit (or larger) RSA key.
func NewJWTService(privateKey *rsa.PrivateKey, ttlHours int) *JWTService {
	if ttlHours <= 0 {
		ttlHours = 8
	}
	return &JWTService{
		privateKey: privateKey,
		ttl:        time.Duration(ttlHours) * time.Hour,
	}
}

// GenerateRSAKey creates an ephemeral 2048-bit RSA key for development.
// Never use this in production — tokens become invalid on every restart.
func GenerateRSAKey() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, 2048)
}

// jwtClaims are the claims embedded in the signed token.
// Roles is a snapshot of role names at login time — for client-side
// initialisation only (so the client knows its roles without a separate
// API call). Server-side permission checks always use RBACStore (authoritative,
// O(1) in-memory lookup) and never trust the JWT roles field.
type jwtClaims struct {
	jwtlib.RegisteredClaims
	Roles []string `json:"roles"`
}

// Issue signs a JWT for u using RS256, embedding roleNames for client-side init.
func (j *JWTService) Issue(u *User, roleNames []string) (*SessionToken, string, error) {
	now := time.Now()
	exp := now.Add(j.ttl)
	tokenID := uuid.New().String()

	claims := jwtClaims{
		RegisteredClaims: jwtlib.RegisteredClaims{
			ID:        tokenID,
			Subject:   u.UserID,
			IssuedAt:  jwtlib.NewNumericDate(now),
			ExpiresAt: jwtlib.NewNumericDate(exp),
		},
		Roles: roleNames,
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodRS256, claims)
	signed, err := token.SignedString(j.privateKey)
	if err != nil {
		return nil, "", fmt.Errorf("sign token: %w", err)
	}

	sess := &SessionToken{
		TokenID:   tokenID,
		UserID:    u.UserID,
		RoleNames: roleNames,
		IssuedAt:  now,
		ExpiresAt: exp,
	}
	return sess, signed, nil
}

// Verify validates the signed string and returns the claims.
// Verification uses the public key derived from the private key.
func (j *JWTService) Verify(signed string) (*jwtClaims, error) {
	pub := &j.privateKey.PublicKey
	token, err := jwtlib.ParseWithClaims(signed, &jwtClaims{}, func(t *jwtlib.Token) (any, error) {
		if _, ok := t.Method.(*jwtlib.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return pub, nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*jwtClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}
	return claims, nil
}
