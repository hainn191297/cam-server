package auth

import (
	"context"
	"fmt"
	"sync"
)

// RBACStore is the contract for Permission / Role / UserRole / RolePermission
// operations. Two implementations exist:
//   - InMemRBACStore  — always present, O(1) map lookups (L1 fast path)
//   - RedisRBACStore  — persistent, used when Redis is available (L2)
//
// LayeredRBACStore wraps both into a write-through cache.
type RBACStore interface {
	// Permissions
	SavePermission(ctx context.Context, p *Permission) error
	GetPermissionByName(ctx context.Context, name string) (*Permission, bool)
	ListPermissions(ctx context.Context) []*Permission

	// Roles
	SaveRole(ctx context.Context, r *RoleModel) error
	GetRoleByName(ctx context.Context, name string) (*RoleModel, bool)
	ListRoles(ctx context.Context) []*RoleModel

	// RolePermission
	AssignPermissionToRole(ctx context.Context, roleID, permID string) error
	RolePermissionNames(ctx context.Context, roleID string) []string

	// UserRole
	AssignRoleToUser(ctx context.Context, userID, roleID string) error
	RemoveRoleFromUser(ctx context.Context, userID, roleID string) error
	UserRoles(ctx context.Context, userID string) []*RoleModel
	UserHasPermission(ctx context.Context, userID, perm string) bool

	// WarmFrom copies all RBAC data from src into this store.
	// Used at startup to populate L1 from L2.
	WarmFrom(ctx context.Context, src RBACStore) error
}

// ── InMemRBACStore ────────────────────────────────────────────────────────────

// InMemRBACStore is the in-memory (L1) implementation of RBACStore.
// All reads are O(1) map lookups under a read lock — safe for the hot
// path inside every HTTP request.
type InMemRBACStore struct {
	mu sync.RWMutex

	permissions map[string]*Permission // keyed by permission_id
	permsByName map[string]*Permission // keyed by name

	roles       map[string]*RoleModel // keyed by role_id
	rolesByName map[string]*RoleModel // keyed by name

	// rolePerms: role_id → set of permission names (fast HasPermission check)
	rolePerms map[string]map[string]bool

	// userRoles: user_id → set of role_id values
	userRoles map[string]map[string]bool
}

func NewInMemRBACStore() *InMemRBACStore {
	return &InMemRBACStore{
		permissions: make(map[string]*Permission),
		permsByName: make(map[string]*Permission),
		roles:       make(map[string]*RoleModel),
		rolesByName: make(map[string]*RoleModel),
		rolePerms:   make(map[string]map[string]bool),
		userRoles:   make(map[string]map[string]bool),
	}
}

func (s *InMemRBACStore) SavePermission(_ context.Context, p *Permission) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissions[p.PermissionID] = p
	s.permsByName[p.Name] = p
	return nil
}

func (s *InMemRBACStore) GetPermissionByName(_ context.Context, name string) (*Permission, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.permsByName[name]
	return p, ok
}

func (s *InMemRBACStore) ListPermissions(_ context.Context) []*Permission {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Permission, 0, len(s.permissions))
	for _, p := range s.permissions {
		out = append(out, p)
	}
	return out
}

func (s *InMemRBACStore) SaveRole(_ context.Context, r *RoleModel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.roles[r.RoleID] = r
	s.rolesByName[r.Name] = r
	return nil
}

func (s *InMemRBACStore) GetRoleByName(_ context.Context, name string) (*RoleModel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.rolesByName[name]
	return r, ok
}

func (s *InMemRBACStore) ListRoles(_ context.Context) []*RoleModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*RoleModel, 0, len(s.roles))
	for _, r := range s.roles {
		out = append(out, r)
	}
	return out
}

func (s *InMemRBACStore) AssignPermissionToRole(_ context.Context, roleID, permID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	perm, ok := s.permissions[permID]
	if !ok {
		return fmt.Errorf("rbac: permission %q not found", permID)
	}
	if _, exists := s.roles[roleID]; !exists {
		return fmt.Errorf("rbac: role %q not found", roleID)
	}
	if s.rolePerms[roleID] == nil {
		s.rolePerms[roleID] = make(map[string]bool)
	}
	s.rolePerms[roleID][perm.Name] = true
	return nil
}

func (s *InMemRBACStore) RolePermissionNames(_ context.Context, roleID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.rolePerms[roleID]))
	for name := range s.rolePerms[roleID] {
		names = append(names, name)
	}
	return names
}

func (s *InMemRBACStore) AssignRoleToUser(_ context.Context, userID, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.roles[roleID]; !exists {
		return fmt.Errorf("rbac: role %q not found", roleID)
	}
	if s.userRoles[userID] == nil {
		s.userRoles[userID] = make(map[string]bool)
	}
	s.userRoles[userID][roleID] = true
	return nil
}

func (s *InMemRBACStore) RemoveRoleFromUser(_ context.Context, userID, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.userRoles[userID] != nil {
		delete(s.userRoles[userID], roleID)
	}
	return nil
}

func (s *InMemRBACStore) UserRoles(_ context.Context, userID string) []*RoleModel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	roleIDs := s.userRoles[userID]
	out := make([]*RoleModel, 0, len(roleIDs))
	for rid := range roleIDs {
		if r, ok := s.roles[rid]; ok {
			out = append(out, r)
		}
	}
	return out
}

func (s *InMemRBACStore) UserHasPermission(_ context.Context, userID, perm string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for rid := range s.userRoles[userID] {
		if s.rolePerms[rid][perm] {
			return true
		}
	}
	return false
}

// WarmFrom copies all RBAC data from src into this store.
// src is typically a RedisRBACStore. Called once at node startup.
func (s *InMemRBACStore) WarmFrom(ctx context.Context, src RBACStore) error {
	perms := src.ListPermissions(ctx)
	for _, p := range perms {
		if err := s.SavePermission(ctx, p); err != nil {
			return fmt.Errorf("rbac.warm: permission %s: %w", p.Name, err)
		}
	}

	roles := src.ListRoles(ctx)
	for _, r := range roles {
		if err := s.SaveRole(ctx, r); err != nil {
			return fmt.Errorf("rbac.warm: role %s: %w", r.Name, err)
		}
		names := src.RolePermissionNames(ctx, r.RoleID)
		for _, name := range names {
			p, ok := s.GetPermissionByName(ctx, name)
			if !ok {
				continue
			}
			_ = s.AssignPermissionToRole(ctx, r.RoleID, p.PermissionID)
		}
	}
	return nil
}
