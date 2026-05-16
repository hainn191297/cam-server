package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis key helpers
//
//	rbac:perm:{perm_id}       → Hash  {permission_id, name, description, created_at}
//	rbac:perms                → Set   of permission_id values
//	rbac:role:{role_id}       → Hash  {role_id, name, description, created_at}
//	rbac:roles                → Set   of role_id values
//	rbac:role_perms:{role_id} → Set   of permission_id values
//	rbac:user_roles:{user_id} → Set   of role_id values

func keyPerm(id string) string     { return "rbac:perm:" + id }
func keyRole(id string) string     { return "rbac:role:" + id }
func keyRolePerms(id string) string { return "rbac:role_perms:" + id }
func keyUserRoles(id string) string { return "rbac:user_roles:" + id }

const keyPerms = "rbac:perms"
const keyRoles = "rbac:roles"

// RedisRBACStore is the persistent (L2) implementation of RBACStore.
// It persists RBAC definitions to Redis so they survive restarts and are
// shared across cluster nodes.
type RedisRBACStore struct {
	rdb *redis.Client
}

func NewRedisRBACStore(rdb *redis.Client) *RedisRBACStore {
	return &RedisRBACStore{rdb: rdb}
}

// ── Permissions ───────────────────────────────────────────────────────────────

func (s *RedisRBACStore) SavePermission(ctx context.Context, p *Permission) error {
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, keyPerm(p.PermissionID), map[string]any{
		"permission_id": p.PermissionID,
		"name":          p.Name,
		"description":   p.Description,
		"created_at":    p.CreatedAt.Format(time.RFC3339),
	})
	pipe.SAdd(ctx, keyPerms, p.PermissionID)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisRBACStore) GetPermissionByName(ctx context.Context, name string) (*Permission, bool) {
	// No name index in Redis — scan all permissions
	perms := s.ListPermissions(ctx)
	for _, p := range perms {
		if p.Name == name {
			return p, true
		}
	}
	return nil, false
}

func (s *RedisRBACStore) ListPermissions(ctx context.Context) []*Permission {
	ids, err := s.rdb.SMembers(ctx, keyPerms).Result()
	if err != nil || len(ids) == 0 {
		return nil
	}
	out := make([]*Permission, 0, len(ids))
	for _, id := range ids {
		fields, err := s.rdb.HGetAll(ctx, keyPerm(id)).Result()
		if err != nil || len(fields) == 0 {
			continue
		}
		p := &Permission{
			PermissionID: fields["permission_id"],
			Name:         fields["name"],
			Description:  fields["description"],
		}
		if t, err := time.Parse(time.RFC3339, fields["created_at"]); err == nil {
			p.CreatedAt = t
		}
		out = append(out, p)
	}
	return out
}

// ── Roles ─────────────────────────────────────────────────────────────────────

func (s *RedisRBACStore) SaveRole(ctx context.Context, r *RoleModel) error {
	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, keyRole(r.RoleID), map[string]any{
		"role_id":     r.RoleID,
		"name":        r.Name,
		"description": r.Description,
		"created_at":  r.CreatedAt.Format(time.RFC3339),
	})
	pipe.SAdd(ctx, keyRoles, r.RoleID)
	_, err := pipe.Exec(ctx)
	return err
}

func (s *RedisRBACStore) GetRoleByName(ctx context.Context, name string) (*RoleModel, bool) {
	roles := s.ListRoles(ctx)
	for _, r := range roles {
		if r.Name == name {
			return r, true
		}
	}
	return nil, false
}

func (s *RedisRBACStore) ListRoles(ctx context.Context) []*RoleModel {
	ids, err := s.rdb.SMembers(ctx, keyRoles).Result()
	if err != nil || len(ids) == 0 {
		return nil
	}
	out := make([]*RoleModel, 0, len(ids))
	for _, id := range ids {
		fields, err := s.rdb.HGetAll(ctx, keyRole(id)).Result()
		if err != nil || len(fields) == 0 {
			continue
		}
		r := &RoleModel{
			RoleID:      fields["role_id"],
			Name:        fields["name"],
			Description: fields["description"],
		}
		if t, err := time.Parse(time.RFC3339, fields["created_at"]); err == nil {
			r.CreatedAt = t
		}
		out = append(out, r)
	}
	return out
}

// ── RolePermission ────────────────────────────────────────────────────────────

func (s *RedisRBACStore) AssignPermissionToRole(ctx context.Context, roleID, permID string) error {
	return s.rdb.SAdd(ctx, keyRolePerms(roleID), permID).Err()
}

// RolePermissionNames returns permission names (not IDs) for a role.
// It resolves IDs through the stored permission hashes.
func (s *RedisRBACStore) RolePermissionNames(ctx context.Context, roleID string) []string {
	permIDs, err := s.rdb.SMembers(ctx, keyRolePerms(roleID)).Result()
	if err != nil || len(permIDs) == 0 {
		return nil
	}
	names := make([]string, 0, len(permIDs))
	for _, pid := range permIDs {
		name, err := s.rdb.HGet(ctx, keyPerm(pid), "name").Result()
		if err == nil && name != "" {
			names = append(names, name)
		}
	}
	return names
}

// ── UserRole ──────────────────────────────────────────────────────────────────

func (s *RedisRBACStore) AssignRoleToUser(ctx context.Context, userID, roleID string) error {
	return s.rdb.SAdd(ctx, keyUserRoles(userID), roleID).Err()
}

func (s *RedisRBACStore) RemoveRoleFromUser(ctx context.Context, userID, roleID string) error {
	return s.rdb.SRem(ctx, keyUserRoles(userID), roleID).Err()
}

func (s *RedisRBACStore) UserRoles(ctx context.Context, userID string) []*RoleModel {
	roleIDs, err := s.rdb.SMembers(ctx, keyUserRoles(userID)).Result()
	if err != nil || len(roleIDs) == 0 {
		return nil
	}
	out := make([]*RoleModel, 0, len(roleIDs))
	for _, rid := range roleIDs {
		fields, err := s.rdb.HGetAll(ctx, keyRole(rid)).Result()
		if err != nil || len(fields) == 0 {
			continue
		}
		r := &RoleModel{
			RoleID:      fields["role_id"],
			Name:        fields["name"],
			Description: fields["description"],
		}
		if t, err := time.Parse(time.RFC3339, fields["created_at"]); err == nil {
			r.CreatedAt = t
		}
		out = append(out, r)
	}
	return out
}

func (s *RedisRBACStore) UserHasPermission(ctx context.Context, userID, perm string) bool {
	roleIDs, err := s.rdb.SMembers(ctx, keyUserRoles(userID)).Result()
	if err != nil {
		return false
	}
	for _, rid := range roleIDs {
		permIDs, err := s.rdb.SMembers(ctx, keyRolePerms(rid)).Result()
		if err != nil {
			continue
		}
		for _, pid := range permIDs {
			name, err := s.rdb.HGet(ctx, keyPerm(pid), "name").Result()
			if err == nil && name == perm {
				return true
			}
		}
	}
	return false
}

// WarmFrom is a no-op for RedisRBACStore — Redis is the source, not a target.
// The layered store calls WarmFrom on the InMemRBACStore instead.
func (s *RedisRBACStore) WarmFrom(_ context.Context, _ RBACStore) error {
	return fmt.Errorf("rbac: WarmFrom not supported on RedisRBACStore (it is the source)")
}
