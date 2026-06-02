package auth

import "time"

// Role constants mirror the user_role enum.
const (
	RoleAdmin   = "admin"
	RoleManager = "manager"
	RoleCashier = "cashier"
)

type User struct {
	ID        int64     `db:"id"        json:"id"`
	Name      string    `db:"name"      json:"name"`
	Phone     *string   `db:"phone"     json:"phone,omitempty"`
	Role      string    `db:"role"      json:"role"`
	PinHash   string    `db:"pin_hash"  json:"-"`
	IsActive  bool      `db:"is_active" json:"is_active"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// UserPublic is the safe projection used on login screens (no pin hash).
type UserPublic struct {
	ID   int64  `db:"id"   json:"id"`
	Name string `db:"name" json:"name"`
	Role string `db:"role" json:"role"`
}

type RefreshToken struct {
	ID        int64     `db:"id"`
	UserID    int64     `db:"user_id"`
	TokenHash string    `db:"token_hash"`
	ExpiresAt time.Time `db:"expires_at"`
	CreatedAt time.Time `db:"created_at"`
}

// LoginInput is bound from both JSON (API) and form (UI) submissions. Users
// authenticate with their phone number + PIN (phone is unique per user), so a
// shop can run many cashiers/managers without exposing the staff list on the
// login screen.
type LoginInput struct {
	Phone string `json:"phone" form:"phone" validate:"required,min=4,max=15"`
	PIN   string `json:"pin"   form:"pin"   validate:"required,min=4,max=6,numeric"`
}

type CreateUserInput struct {
	Name  string `json:"name"  form:"name"  validate:"required,min=2,max=100"`
	Phone string `json:"phone" form:"phone" validate:"required,min=4,max=15"`
	Role  string `json:"role"  form:"role"  validate:"required,oneof=admin manager cashier"`
	PIN   string `json:"pin"   form:"pin"   validate:"required,min=4,max=6,numeric"`
}

type RefreshInput struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

// TokenPair is returned to API clients on login/refresh.
type TokenPair struct {
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	ExpiresAt    time.Time  `json:"expires_at"`
	User         UserPublic `json:"user"`
}

// HomePath returns the landing route for a role after login.
func HomePath(role string) string {
	if role == RoleCashier {
		return "/cashier"
	}
	return "/admin"
}
