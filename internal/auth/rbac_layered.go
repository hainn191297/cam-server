package auth

import (
	"context"
	"fmt"

	"github.com/sirupsen/logrus"
)

// LayeredRBACStore is a write-through cache:
//   - Reads  → always from mem (L1, O(1), no network)
//   - Writes → redis (L2) first, then mem; if Redis fails the write is rejected
//   - WarmFrom loads all data from redis → mem at node startup
type LayeredRBACStore struct {
	mem   *InMemRBACStore
	redis *RedisRBACStore
}

func NewLayeredRBACStore(mem *InMemRBACStore, redis *RedisRBACStore) *LayeredRBACStore {
	return &LayeredRBACStore{mem: mem, redis: redis}
}

// ── Reads — always from L1 ────────────────────────────────────────────────────

func (s *LayeredRBACStore) GetPermissionByName(ctx context.Context, name string) (*Permission, bool) {
	return s.mem.GetPermissionByName(ctx, name)
}

func (s *LayeredRBACStore) ListPermissions(ctx context.Context) []*Permission {
	return s.mem.ListPermissions(ctx)
}

func (s *LayeredRBACStore) GetRoleByName(ctx context.Context, name string) (*RoleModel, bool) {
	return s.mem.GetRoleByName(ctx, name)
}

func (s *LayeredRBACStore) ListRoles(ctx context.Context) []*RoleModel {
	return s.mem.ListRoles(ctx)
}

func (s *LayeredRBACStore) RolePermissionNames(ctx context.Context, roleID string) []string {
	return s.mem.RolePermissionNames(ctx, roleID)
}

func (s *LayeredRBACStore) UserRoles(ctx context.Context, userID string) []*RoleModel {
	return s.mem.UserRoles(ctx, userID)
}

func (s *LayeredRBACStore) UserHasPermission(ctx context.Context, userID, perm string) bool {
	return s.mem.UserHasPermission(ctx, userID, perm)
}

// ── Writes — L2 first, then L1 ───────────────────────────────────────────────

func (s *LayeredRBACStore) SavePermission(ctx context.Context, p *Permission) error {
	if err := s.redis.SavePermission(ctx, p); err != nil {
		return fmt.Errorf("rbac.layered: redis SavePermission: %w", err)
	}
	return s.mem.SavePermission(ctx, p)
}

func (s *LayeredRBACStore) SaveRole(ctx context.Context, r *RoleModel) error {
	if err := s.redis.SaveRole(ctx, r); err != nil {
		return fmt.Errorf("rbac.layered: redis SaveRole: %w", err)
	}
	return s.mem.SaveRole(ctx, r)
}

func (s *LayeredRBACStore) AssignPermissionToRole(ctx context.Context, roleID, permID string) error {
	if err := s.redis.AssignPermissionToRole(ctx, roleID, permID); err != nil {
		return fmt.Errorf("rbac.layered: redis AssignPermissionToRole: %w", err)
	}
	return s.mem.AssignPermissionToRole(ctx, roleID, permID)
}

func (s *LayeredRBACStore) AssignRoleToUser(ctx context.Context, userID, roleID string) error {
	if err := s.redis.AssignRoleToUser(ctx, userID, roleID); err != nil {
		return fmt.Errorf("rbac.layered: redis AssignRoleToUser: %w", err)
	}
	return s.mem.AssignRoleToUser(ctx, userID, roleID)
}

func (s *LayeredRBACStore) RemoveRoleFromUser(ctx context.Context, userID, roleID string) error {
	if err := s.redis.RemoveRoleFromUser(ctx, userID, roleID); err != nil {
		return fmt.Errorf("rbac.layered: redis RemoveRoleFromUser: %w", err)
	}
	return s.mem.RemoveRoleFromUser(ctx, userID, roleID)
}

// WarmFrom loads all RBAC data from redis into mem.
// src is ignored — LayeredRBACStore always warms from its own redis instance.
func (s *LayeredRBACStore) WarmFrom(ctx context.Context, _ RBACStore) error {
	logrus.Info("rbac.layered: warming L1 from Redis")
	if err := s.mem.WarmFrom(ctx, s.redis); err != nil {
		return fmt.Errorf("rbac.layered: warm: %w", err)
	}
	logrus.Info("rbac.layered: warm complete")
	return nil
}
