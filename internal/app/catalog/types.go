package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
)

var (
	ErrDraft        = errors.New("draft_conflict")
	ErrVersion      = errors.New("version_conflict")
	ErrReferenced   = errors.New("version_referenced")
	ErrConfig       = errors.New("invalid_definition")
	ErrNameConflict = errors.New("definition_name_conflict")
)

type IndexWriteError struct {
	Err       error
	Committed bool
}

func (e *IndexWriteError) Error() string { return e.Err.Error() }
func (e *IndexWriteError) Unwrap() error { return e.Err }

const MaxDocument = 256 << 10
const MaxResource = 8 << 20

type Definition struct {
	ID               string     `json:"id"`
	Kind             string     `json:"kind"`
	Name             string     `json:"name"`
	Archived         bool       `json:"archived"`
	DefaultVersionID string     `json:"default_version_id"`
	Visibility       Visibility `json:"visibility,omitempty"`
	LatestVersion    string     `json:"latest_version,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at,omitzero"`
	Description      *string    `json:"description,omitempty"`
}

type Visibility string

const (
	VisibilitySpace  Visibility = "space"
	VisibilityPublic Visibility = "public"
)

type Asset struct {
	Path      string `json:"path"`
	ObjectID  string `json:"object_id"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
	Package   bool   `json:"package,omitempty"`
}
type Document struct {
	Markdown    string   `json:"markdown"`
	ConfigYAML  string   `json:"config_yaml"`
	Assets      []Asset  `json:"assets,omitzero"`
	Directories []string `json:"directories,omitempty"`
}
type Draft struct {
	Definition
	Revision  int64    `json:"revision"`
	Document  Document `json:"document"`
	ContentID string   `json:"-"`
}
type SkillRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

const DefaultMaxTokens = 32000

type ContextConfig struct {
	MaxInputTokens int    `json:"max_input_tokens"`
	MaxTokens      int    `json:"max_tokens"`
	Compression    string `json:"compression"`
}

// Normalize applies the default output budget.
func (c *ContextConfig) Normalize() {
	if c.MaxTokens == 0 {
		c.MaxTokens = DefaultMaxTokens
	}
}

func (c *ContextConfig) UnmarshalJSON(raw []byte) error {
	type contextConfig ContextConfig
	var value contextConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&value); err != nil {
		return err
	}
	*c = ContextConfig(value)
	c.Normalize()
	return nil
}

type Config struct {
	SchemaVersion int           `json:"schema_version"`
	Name          string        `json:"name"`
	Model         string        `json:"model"`
	Skills        []SkillRef    `json:"skills"`
	Tools         []string      `json:"tools"`
	Context       ContextConfig `json:"context"`
	Limits        struct {
		MaxSteps       int `json:"max_steps"`
		TimeoutSeconds int `json:"timeout_seconds"`
	} `json:"limits"`
}

func (c Config) SupportsModel(model identity.Model) bool {
	c.Context.Normalize()
	return model.Enabled && model.Provider == "ark" &&
		c.Context.MaxInputTokens+c.Context.MaxTokens <= model.ContextWindow &&
		c.Context.MaxTokens <= model.MaxOutputTokens
}

type Binding struct {
	SkillID   string `json:"skill_id"`
	VersionID string `json:"version_id"`
	Version   string `json:"version"`
	SHA256    string `json:"sha256"`
}
type Bundle struct {
	FormatVersion int             `json:"format_version"`
	DefinitionID  string          `json:"definition_id"`
	Kind          string          `json:"kind"`
	Name          string          `json:"name"`
	Revision      int64           `json:"revision"`
	Document      Document        `json:"document"`
	Config        *Config         `json:"config"`
	Model         *identity.Model `json:"model"`
	Bindings      []Binding       `json:"bindings"`
}
type Version struct {
	ID           string    `json:"id"`
	DefinitionID string    `json:"definition_id"`
	Label        string    `json:"label"`
	Debug        bool      `json:"debug"`
	Archived     bool      `json:"archived"`
	ContentID    string    `json:"content_id"`
	SHA256       string    `json:"sha256"`
	CreatedAt    time.Time `json:"created_at"`
	Bundle       Bundle    `json:"bundle"`
}
type Reference struct {
	ID           string `json:"id"`
	DefinitionID string `json:"definition_id"`
	Name         string `json:"name"`
	Label        string `json:"label"`
	Debug        bool   `json:"debug"`
}
type Repository interface {
	CatalogCreate(context.Context, string, string, Draft) error
	CatalogList(context.Context, string, string, string, identity.Query) ([]Definition, error)
	CatalogDraft(context.Context, string, string, string, string) (Draft, error)
	CatalogSave(context.Context, string, string, Draft) error
	CatalogPrepare(context.Context, string, string, string, int64) (Bundle, error)
	CatalogPublish(context.Context, string, string, *Version) error
	CatalogVersions(context.Context, string, string, string, string, bool, identity.Query) ([]Version, error)
	CatalogVersion(context.Context, string, string, string, string, string) (Version, error)
	CatalogReferences(context.Context, string, string, string, string, string, identity.Query) ([]Reference, error)
	CatalogState(context.Context, string, string, string, string, string, string, bool) error
}
