package relay

import (
	"fmt"
	"time"

	jwtlib "github.com/golang-jwt/jwt/v5"
	"github.com/sirupsen/logrus"
)

// TokenService issues and validates relay session tokens.
// Uses a separate secret from the auth JWT.
type TokenService struct {
	secret []byte
}

func NewTokenService(secret string) *TokenService {
	return &TokenService{secret: []byte(secret)}
}

type relayTokenClaims struct {
	jwtlib.RegisteredClaims
	RelaySessionID string `json:"relay_session_id"`
	StreamID       string `json:"stream_id"`
	CamID          string `json:"cam_id"`
	Protocol       string `json:"protocol"`
}

func (ts *TokenService) Issue(sess *Session) (string, error) {
	claims := relayTokenClaims{
		RegisteredClaims: jwtlib.RegisteredClaims{
			ID:        sess.RelaySessionID,
			ExpiresAt: jwtlib.NewNumericDate(sess.TokenExpiresAt),
			IssuedAt:  jwtlib.NewNumericDate(time.Now()),
		},
		RelaySessionID: sess.RelaySessionID,
		StreamID:       sess.StreamID,
		CamID:          sess.CamID,
		Protocol:       string(sess.Protocol),
	}
	token := jwtlib.NewWithClaims(jwtlib.SigningMethodHS256, claims)
	signed, err := token.SignedString(ts.secret)
	if err != nil {
		return "", fmt.Errorf("relay token sign: %w", err)
	}
	return signed, nil
}

func (ts *TokenService) Verify(signed string) (*relayTokenClaims, error) {
	token, err := jwtlib.ParseWithClaims(signed, &relayTokenClaims{}, func(t *jwtlib.Token) (any, error) {
		if _, ok := t.Method.(*jwtlib.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return ts.secret, nil
	})
	if err != nil {
		logrus.WithError(err).Debug("relay.token: verify failed")
		return nil, err
	}
	claims, ok := token.Claims.(*relayTokenClaims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid relay token claims")
	}
	return claims, nil
}
