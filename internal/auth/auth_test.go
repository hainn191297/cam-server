package auth_test

import (
	"context"
	"testing"

	"go-cam-server/internal/auth"
)

func newTestService(t *testing.T) (*auth.Service, string) {
	t.Helper()
	store := auth.NewStore()
	jwtSvc := auth.NewJWTService("test-secret-key", 8)
	svc := auth.NewService(store, jwtSvc)
	pass, err := svc.SeedAdmin("admin", "password123")
	if err != nil {
		t.Fatalf("SeedAdmin: %v", err)
	}
	_ = pass
	return svc, "password123"
}

func TestLogin_ValidCredentials(t *testing.T) {
	svc, password := newTestService(t)
	resp, sess, err := svc.Login(context.Background(), auth.LoginRequest{
		Username: "admin",
		Password: password,
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if resp.Token == "" {
		t.Error("expected non-empty token")
	}
	if sess.TokenID == "" {
		t.Error("expected non-empty TokenID")
	}
	if sess.UserID == "" {
		t.Error("expected non-empty UserID")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, err := svc.Login(context.Background(), auth.LoginRequest{
		Username: "admin",
		Password: "wrongpassword",
	})
	if err != auth.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestLogin_UnknownUser(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, err := svc.Login(context.Background(), auth.LoginRequest{
		Username: "nobody",
		Password: "any",
	})
	if err != auth.ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}

func TestValidateToken_Valid(t *testing.T) {
	svc, password := newTestService(t)
	resp, _, err := svc.Login(context.Background(), auth.LoginRequest{Username: "admin", Password: password})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	sess, user, err := svc.ValidateToken(context.Background(), resp.Token)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if user.Username != "admin" {
		t.Errorf("expected username admin, got %s", user.Username)
	}
	if sess.Role != auth.RoleAdmin {
		t.Errorf("expected admin role, got %s", sess.Role)
	}
}

func TestValidateToken_Invalid(t *testing.T) {
	svc, _ := newTestService(t)
	_, _, err := svc.ValidateToken(context.Background(), "not.a.jwt")
	if err != auth.ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestLogout_TokenRevoked(t *testing.T) {
	svc, password := newTestService(t)
	resp, sess, err := svc.Login(context.Background(), auth.LoginRequest{Username: "admin", Password: password})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	svc.Logout(context.Background(), sess.TokenID)

	_, _, err = svc.ValidateToken(context.Background(), resp.Token)
	if err != auth.ErrTokenInvalid {
		t.Errorf("expected ErrTokenInvalid after logout, got %v", err)
	}
}

func TestSeedAdmin_Idempotent(t *testing.T) {
	svc, _ := newTestService(t)
	// Second seed must not error and must not overwrite the existing user.
	_, err := svc.SeedAdmin("admin", "differentpassword")
	if err != nil {
		t.Fatalf("second SeedAdmin: %v", err)
	}
	// Original password must still work.
	_, _, err = svc.Login(context.Background(), auth.LoginRequest{Username: "admin", Password: "password123"})
	if err != nil {
		t.Errorf("original credentials must still work after second SeedAdmin: %v", err)
	}
}

func TestRolePermissions(t *testing.T) {
	if !auth.RoleAdmin.HasPermission("camera:manage") {
		t.Error("admin should have camera:manage")
	}
	if auth.RoleOperator.HasPermission("camera:manage") {
		t.Error("operator must not have camera:manage")
	}
	if !auth.RoleOperator.HasPermission("camera:view") {
		t.Error("operator should have camera:view")
	}
}
