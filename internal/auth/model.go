package auth

import "time"

// ── RBAC structs ─────────────────────────────────────────────────────────────

// Permission is a named capability that can be assigned to roles.
type Permission struct {
	PermissionID string    `json:"permission_id"` // uuid v7
	Name         string    `json:"name"`           // e.g. "camera:view"
	Description  string    `json:"description"`
	CreatedAt    time.Time `json:"created_at"`
}

// RoleModel is a named collection of permissions.
// Named RoleModel to avoid collision with the legacy Role string type.
type RoleModel struct {
	RoleID      string    `json:"role_id"` // uuid v7
	Name        string    `json:"name"`    // e.g. "admin", "operator"
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// UserRole is the join between a user and a role (many-to-many).
type UserRole struct {
	UserID string `json:"user_id"`
	RoleID string `json:"role_id"`
}

// RolePermission is the join between a role and a permission (many-to-many).
type RolePermission struct {
	RoleID       string `json:"role_id"`
	PermissionID string `json:"permission_id"`
}

// ── User ──────────────────────────────────────────────────────────────────────

// User is the auth domain user. Roles are stored separately in the UserRole
// join table and looked up via RBACStore — there is no Role field here.
type User struct {
	UserID       string    `json:"user_id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	IsActive     bool      `json:"is_active"`
}

// ── Session ───────────────────────────────────────────────────────────────────

// SessionToken represents an issued JWT tracked server-side for revocation.
type SessionToken struct {
	TokenID       string     `json:"token_id"`
	UserID        string     `json:"user_id"`
	RoleNames     []string   `json:"role_names"` // snapshot at login for logging
	IssuedAt      time.Time  `json:"issued_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	InvalidatedAt *time.Time `json:"invalidated_at,omitempty"`
}

// ── HTTP request/response shapes ──────────────────────────────────────────────

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      UserInfo  `json:"user"`
}

// UserInfo is the minimal user summary embedded in the login response.
type UserInfo struct {
	UserID   string   `json:"user_id"`
	Username string   `json:"username"`
	Roles    []string `json:"roles"` // was single Role string
}

// SessionInfo is returned by GET /api/v1/auth/session.
type SessionInfo struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	Roles       []string  `json:"roles"`       // was single Role string
	Permissions []string  `json:"permissions"` // effective union of all role permissions
	ExpiresAt   time.Time `json:"expires_at"`
}

// ── User management request / response shapes ─────────────────────────────────

// CreateUserRequest is the body for POST /api/v1/users.
type CreateUserRequest struct {
	Username string   `json:"username"`
	Password string   `json:"password"`
	Roles    []string `json:"roles"` // role names; defaults to ["operator"] if empty
}

// UpdateUserRolesRequest is the body for PATCH /api/v1/users/{id}/roles.
type UpdateUserRolesRequest struct {
	Roles []string `json:"roles"` // full replacement — replaces all current roles
}

// UserDetail is the canonical user response shape (includes resolved role names).
type UserDetail struct {
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Roles     []string  `json:"roles"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ── JWT Claims ────────────────────────────────────────────────────────────────

// Claims are the JWT claims stored in the signed token.
// Role is intentionally omitted — permissions are looked up from the RBAC
// store on every request (in-memory, O(1)).
type Claims struct {
	TokenID string `json:"jti"`
	UserID  string `json:"sub"`
}
