package identity

import (
	"context"
	"errors"
	"time"
)

var (
	ErrUnauthenticated = errors.New("unauthenticated")
	ErrForbidden       = errors.New("forbidden")
	ErrNotFound        = errors.New("not_found")
	ErrConflict        = errors.New("conflict")
	ErrInvalid         = errors.New("invalid_request")
	ErrCapability      = errors.New("unsupported_capability")
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}
type Space struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	IsOwner   bool      `json:"is_owner"`
	CreatedAt time.Time `json:"created_at"`
}
type Member struct {
	User
	IsOwner bool `json:"is_owner"`
}
type Model struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	Provider        string  `json:"provider"`
	ModelID         string  `json:"model_id"`
	BaseURL         string  `json:"base_url"`
	APIKey          string  `json:"api_key,omitempty"`
	SecretBindingID string  `json:"-"`
	KeyID           string  `json:"-"`
	Ciphertext      []byte  `json:"-"`
	ContextWindow   int     `json:"context_window"`
	MaxOutputTokens int     `json:"max_output_tokens"`
	Temperature     float64 `json:"temperature"`
	Enabled         bool    `json:"enabled"`
	Revision        int64   `json:"revision"`
}
type OpenAPIToken struct {
	ID         string     `json:"id"`
	SpaceID    string     `json:"-"`
	IssuerID   string     `json:"-"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	RevokedAt  *time.Time `json:"revoked_at"`
	TokenHash  []byte     `json:"-"`
	KeyID      string     `json:"-"`
	Ciphertext []byte     `json:"-"`
}
type CreatedOpenAPIToken struct {
	OpenAPIToken
	Token string `json:"token"`
}
type OpenAPITokenSecret struct {
	ID    string `json:"id"`
	Token string `json:"token"`
}
type BearerPrincipal struct {
	User    User
	SpaceID string
	TokenID string
}
type Page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
type Query struct {
	After string
	Limit int
}

// Repository operations authorize inside their transaction, with space locking
// shared by management and removal. No cached membership authorizes a request.
type Repository interface {
	CreateUser(context.Context, User, []byte) error
	Password(context.Context, string) (User, []byte, error)
	SaveSession(context.Context, []byte, string, time.Time) error
	Session(context.Context, []byte) (User, error)
	DeleteSession(context.Context, []byte) error
	CheckAccess(context.Context, string, string, bool) error
	CreateSpace(context.Context, string, Space) error
	Spaces(context.Context, string, Query) ([]Space, error)
	Members(context.Context, string, string, Query) ([]Member, error)
	AddMember(context.Context, string, string, string) error
	RemoveMember(context.Context, string, string, string) error
	Models(context.Context, string, string, Query, bool) ([]Model, error)
	Model(context.Context, string, string, string) (Model, error)
	SaveModel(context.Context, string, string, Model, bool) error
	ResolveModelSecret(context.Context, string, string, string) (Model, error)
	OpenAPITokens(context.Context, string, string, Query) ([]OpenAPIToken, error)
	CreateOpenAPIToken(context.Context, string, string, OpenAPIToken) error
	OpenAPITokenSecret(context.Context, string, string, string) (OpenAPIToken, error)
	RenewOpenAPIToken(context.Context, string, string, string, time.Time) (OpenAPIToken, error)
	RevokeOpenAPIToken(context.Context, string, string, string, time.Time) error
	AuthenticateBearerToken(context.Context, []byte) (BearerPrincipal, error)
}

type Cipher interface {
	Seal(space, id string, plaintext []byte) (keyID string, ciphertext []byte, err error)
	Open(space, id, keyID string, ciphertext []byte) ([]byte, error)
}
