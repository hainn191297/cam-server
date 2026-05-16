package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/sirupsen/logrus"
)

type contextKeyType string

const (
	contextKeySession contextKeyType = "auth_session"
	contextKeyUser    contextKeyType = "auth_user"
)

// Middleware validates the Bearer token on every protected request.
func (s *Service) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signed := extractBearer(r)
		if signed == "" {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", "missing token")
			return
		}
		sess, u, err := s.ValidateToken(r.Context(), signed)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		ctx := context.WithValue(r.Context(), contextKeySession, sess)
		ctx = context.WithValue(ctx, contextKeyUser, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePermission returns middleware that checks a specific permission.
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := UserFromContext(r.Context())
			if !ok || !u.Role.HasPermission(perm) {
				writeAuthError(w, http.StatusForbidden, "forbidden", "missing permission: "+perm)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// SessionFromContext returns the validated session from context.
func SessionFromContext(ctx context.Context) (*SessionToken, bool) {
	s, ok := ctx.Value(contextKeySession).(*SessionToken)
	return s, ok
}

// UserFromContext returns the authenticated user from context.
func UserFromContext(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(contextKeyUser).(*User)
	return u, ok
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func writeAuthError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	logrus.WithFields(logrus.Fields{"code": code, "status": status}).Debug("auth: rejected request")
	fmt.Fprintf(w, `{"code":%q,"message":%q}`, code, msg)
}
