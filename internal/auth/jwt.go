package auth

import (
	"fmt"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

type JWTService struct {
	secret []byte
	ttl    time.Duration
}

func NewJWTService(secret string, ttlHours int) *JWTService {
	if ttlHours <= 0 {
		ttlHours = 8
	}
	return &JWTService{
		secret: []byte(secret),
		ttl:    time.Duration(ttlHours) * time.Hour,
	}
}

type jwtClaims struct {
	jwtlib.RegisteredClaims
	Role Role `json:"role"`
}

func (j *JWTService) Issue(u *User) (*SessionToken, string, error) {
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
		Role: u.Role,
	}

	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims)
	signed, err := token.SignedString(j.secret)
	if err != nil {
		return nil, "", fmt.Errorf("sign token: %w", err)
	}

	sess := &SessionToken{
		TokenID:   tokenID,
		UserID:    u.UserID,
		Role:      u.Role,
		IssuedAt:  now,
		ExpiresAt: exp,
	}
	return sess, signed, nil
}

// Verify validates the signed string and returns the claims.
func (j *JWTService) Verify(signed string) (*jwtClaims, error) {
	token, err := jwtlib.ParseWithClaims(signed, &jwtClaims{}, func(t *jwtlib.Token) (any, error) {
		if _, ok := t.Method.(*jwtlib.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return j.secret, nil
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
