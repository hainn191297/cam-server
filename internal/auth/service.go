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
	ErrUserNotFound       = errors.New("user_not_found")
	ErrUserAlreadyExists  = errors.New("user_already_exists")
	ErrRoleNotFound       = errors.New("role_not_found")
)

type Service struct {
	store *Store
	jwt   *JWTService
	rbac  RBACStore
}

func NewService(store *Store, jwt *JWTService, rbac RBACStore) *Service {
	return &Service{store: store, jwt: jwt, rbac: rbac}
}

// SeedRBAC seeds the canonical permissions and roles if they don't already
// exist. Idempotent — safe to call on every startup.
func (s *Service) SeedRBAC(ctx context.Context) error {
	type permSeed struct {
		name, desc string
	}
	permSeeds := []permSeed{
		{"camera:view", "View camera feeds and live sessions"},
		{"camera:manage", "Create, update, and delete cameras"},
		{"relay:manage", "Manage relay sessions"},
		{"user:manage", "Create, update, and delete users"},
	}

	for _, ps := range permSeeds {
		if _, exists := s.rbac.GetPermissionByName(ctx, ps.name); exists {
			continue // already seeded
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("rbac.seed: generate permission id: %w", err)
		}
		if err := s.rbac.SavePermission(ctx, &Permission{
			PermissionID: id.String(),
			Name:         ps.name,
			Description:  ps.desc,
			CreatedAt:    time.Now(),
		}); err != nil {
			return fmt.Errorf("rbac.seed: permission %s: %w", ps.name, err)
		}
	}

	type roleSeed struct {
		name, desc  string
		permissions []string
	}
	roleSeeds := []roleSeed{
		{"admin", "Full system access", []string{"camera:view", "camera:manage", "relay:manage", "user:manage"}},
		{"operator", "Camera viewing access", []string{"camera:view"}},
	}

	for _, rs := range roleSeeds {
		if _, exists := s.rbac.GetRoleByName(ctx, rs.name); exists {
			continue // already seeded
		}
		id, err := uuid.NewV7()
		if err != nil {
			return fmt.Errorf("rbac.seed: generate role id: %w", err)
		}
		role := &RoleModel{
			RoleID:      id.String(),
			Name:        rs.name,
			Description: rs.desc,
			CreatedAt:   time.Now(),
		}
		if err := s.rbac.SaveRole(ctx, role); err != nil {
			return fmt.Errorf("rbac.seed: role %s: %w", rs.name, err)
		}
		for _, permName := range rs.permissions {
			perm, ok := s.rbac.GetPermissionByName(ctx, permName)
			if !ok {
				return fmt.Errorf("rbac.seed: permission %s not found for role %s", permName, rs.name)
			}
			if err := s.rbac.AssignPermissionToRole(ctx, role.RoleID, perm.PermissionID); err != nil {
				return fmt.Errorf("rbac.seed: assign %s→%s: %w", permName, rs.name, err)
			}
		}
		logrus.WithField("role", rs.name).Info("rbac.seed: role created")
	}
	return nil
}

// SeedAdmin creates the default admin account if it doesn't exist.
// If password is empty, a random one is generated and printed once.
func (s *Service) SeedAdmin(ctx context.Context, username, password string) (string, error) {
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
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate user id: %w", err)
	}
	u := &User{
		UserID:       id.String(),
		Username:     username,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
		IsActive:     true,
	}
	if err := s.store.AddUser(u); err != nil {
		return "", err
	}

	// Assign admin role
	adminRole, ok := s.rbac.GetRoleByName(ctx, "admin")
	if !ok {
		return "", fmt.Errorf("auth.seed: admin role not found — call SeedRBAC first")
	}
	if err := s.rbac.AssignRoleToUser(ctx, u.UserID, adminRole.RoleID); err != nil {
		return "", fmt.Errorf("auth.seed: assign admin role: %w", err)
	}

	logrus.WithField("user_id", u.UserID).Info("auth.seed: admin user created")
	return password, nil
}

// Login verifies credentials and issues a session token.
func (s *Service) Login(ctx context.Context, req LoginRequest) (*LoginResponse, *SessionToken, error) {
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

	roles := s.rbac.UserRoles(ctx, u.UserID)
	roleNames := make([]string, len(roles))
	for i, r := range roles {
		roleNames[i] = r.Name
	}

	sess, signed, err := s.jwt.Issue(u, roleNames)
	if err != nil {
		return nil, nil, fmt.Errorf("issue token: %w", err)
	}
	s.store.SaveToken(sess)

	logrus.WithFields(logrus.Fields{
		"user_id":  u.UserID,
		"token_id": sess.TokenID,
		"roles":    roleNames,
	}).Info("auth.login: success")

	return &LoginResponse{
		Token:     signed,
		ExpiresAt: sess.ExpiresAt,
		User:      UserInfo{UserID: u.UserID, Username: u.Username, Roles: roleNames},
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
func (s *Service) GetSessionInfo(ctx context.Context, sess *SessionToken, u *User) *SessionInfo {
	roles := s.rbac.UserRoles(ctx, u.UserID)
	roleNames := make([]string, len(roles))
	permSet := make(map[string]bool)
	for i, r := range roles {
		roleNames[i] = r.Name
		for _, pn := range s.rbac.RolePermissionNames(ctx, r.RoleID) {
			permSet[pn] = true
		}
	}
	perms := make([]string, 0, len(permSet))
	for p := range permSet {
		perms = append(perms, p)
	}
	return &SessionInfo{
		UserID:      u.UserID,
		Username:    u.Username,
		Roles:       roleNames,
		Permissions: perms,
		ExpiresAt:   sess.ExpiresAt,
	}
}

// UserHasPermission is the public permission check used by middleware.
func (s *Service) UserHasPermission(ctx context.Context, userID, perm string) bool {
	return s.rbac.UserHasPermission(ctx, userID, perm)
}

// ── User management ───────────────────────────────────────────────────────────

// CreateUser creates a new user and assigns the requested roles.
// Roles defaults to ["operator"] when the request omits them.
func (s *Service) CreateUser(ctx context.Context, req CreateUserRequest) (*UserDetail, error) {
	if req.Username == "" {
		return nil, fmt.Errorf("%w: username is required", ErrInvalidCredentials)
	}
	if len(req.Password) < 8 {
		return nil, fmt.Errorf("%w: password must be at least 8 characters", ErrInvalidCredentials)
	}
	if _, exists := s.store.GetByUsername(req.Username); exists {
		return nil, ErrUserAlreadyExists
	}

	roleNames := req.Roles
	if len(roleNames) == 0 {
		roleNames = []string{"operator"}
	}
	roles, err := s.resolveRoles(ctx, roleNames)
	if err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("generate user id: %w", err)
	}
	now := time.Now()
	u := &User{
		UserID:       id.String(),
		Username:     req.Username,
		PasswordHash: string(hash),
		CreatedAt:    now,
		UpdatedAt:    now,
		IsActive:     true,
	}
	if err := s.store.AddUser(u); err != nil {
		return nil, ErrUserAlreadyExists
	}
	for _, r := range roles {
		if err := s.rbac.AssignRoleToUser(ctx, u.UserID, r.RoleID); err != nil {
			return nil, fmt.Errorf("assign role %s: %w", r.Name, err)
		}
	}

	logrus.WithFields(logrus.Fields{"user_id": u.UserID, "roles": roleNames}).Info("user.create")
	return s.userDetail(ctx, u), nil
}

// GetUser returns a single user by ID.
func (s *Service) GetUser(ctx context.Context, userID string) (*UserDetail, error) {
	u, ok := s.store.GetByID(userID)
	if !ok {
		return nil, ErrUserNotFound
	}
	return s.userDetail(ctx, u), nil
}

// ListAllUsers returns all users with their resolved roles.
func (s *Service) ListAllUsers(ctx context.Context) []*UserDetail {
	users := s.store.ListUsers()
	out := make([]*UserDetail, len(users))
	for i, u := range users {
		out[i] = s.userDetail(ctx, u)
	}
	return out
}

// DeleteUser removes a user and their role assignments.
func (s *Service) DeleteUser(ctx context.Context, userID string) error {
	// Remove all role assignments first.
	for _, r := range s.rbac.UserRoles(ctx, userID) {
		_ = s.rbac.RemoveRoleFromUser(ctx, userID, r.RoleID)
	}
	if !s.store.DeleteUser(userID) {
		return ErrUserNotFound
	}
	logrus.WithField("user_id", userID).Info("user.delete")
	return nil
}

// UpdateUserRoles replaces a user's roles with the provided set.
func (s *Service) UpdateUserRoles(ctx context.Context, userID string, roleNames []string) (*UserDetail, error) {
	u, ok := s.store.GetByID(userID)
	if !ok {
		return nil, ErrUserNotFound
	}
	newRoles, err := s.resolveRoles(ctx, roleNames)
	if err != nil {
		return nil, err
	}

	// Remove current roles.
	for _, r := range s.rbac.UserRoles(ctx, userID) {
		_ = s.rbac.RemoveRoleFromUser(ctx, userID, r.RoleID)
	}
	// Assign new roles.
	for _, r := range newRoles {
		if err := s.rbac.AssignRoleToUser(ctx, userID, r.RoleID); err != nil {
			return nil, fmt.Errorf("assign role %s: %w", r.Name, err)
		}
	}

	logrus.WithFields(logrus.Fields{"user_id": userID, "roles": roleNames}).Info("user.roles.update")
	return s.userDetail(ctx, u), nil
}

// resolveRoles looks up RoleModel values for a slice of role names.
// Returns ErrRoleNotFound if any name is unknown.
func (s *Service) resolveRoles(ctx context.Context, names []string) ([]*RoleModel, error) {
	out := make([]*RoleModel, 0, len(names))
	for _, name := range names {
		r, ok := s.rbac.GetRoleByName(ctx, name)
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrRoleNotFound, name)
		}
		out = append(out, r)
	}
	return out, nil
}

// userDetail builds a UserDetail response for u, resolving roles from the store.
func (s *Service) userDetail(ctx context.Context, u *User) *UserDetail {
	roles := s.rbac.UserRoles(ctx, u.UserID)
	names := make([]string, len(roles))
	for i, r := range roles {
		names[i] = r.Name
	}
	return &UserDetail{
		UserID:    u.UserID,
		Username:  u.Username,
		Roles:     names,
		IsActive:  u.IsActive,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
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
