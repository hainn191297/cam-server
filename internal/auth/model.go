package auth

import "time"

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
)

var rolePermissions = map[Role][]string{
	RoleAdmin:    {"camera:view", "camera:manage", "relay:manage", "user:manage"},
	RoleOperator: {"camera:view"},
}

func (r Role) HasPermission(perm string) bool {
	for _, p := range rolePermissions[r] {
		if p == perm {
			return true
		}
	}
	return false
}

func (r Role) Permissions() []string { return rolePermissions[r] }

type User struct {
	UserID       string    `json:"user_id"`
	Username     string    `json:"username"`
	PasswordHash string    `json:"-"`
	Role         Role      `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	IsActive     bool      `json:"is_active"`
}

type SessionToken struct {
	TokenID       string     `json:"token_id"`
	UserID        string     `json:"user_id"`
	Role          Role       `json:"role"`
	IssuedAt      time.Time  `json:"issued_at"`
	ExpiresAt     time.Time  `json:"expires_at"`
	InvalidatedAt *time.Time `json:"invalidated_at,omitempty"`
}

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      UserInfo  `json:"user"`
}

type UserInfo struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
}

type SessionInfo struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	Role        Role      `json:"role"`
	Permissions []string  `json:"permissions"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Claims are the JWT claims stored in the signed token.
type Claims struct {
	TokenID string `json:"jti"`
	UserID  string `json:"sub"`
	Role    Role   `json:"role"`
}
