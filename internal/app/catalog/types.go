package catalog

import (
	"context"
	"errors"
	"time"

	"github.com/imalimin/mass/internal/app/identity"
)

var (
	ErrDraft      = errors.New("draft_conflict")
	ErrVersion    = errors.New("version_conflict")
	ErrReferenced = errors.New("version_referenced")
	ErrConfig     = errors.New("invalid_definition")
)

const MaxDocument = 256 << 10
const MaxResource = 8 << 20

type Definition struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Archived         bool   `json:"archived"`
	DefaultVersionID string `json:"default_version_id"`
}
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
	ID      string `json:"id" yaml:"id"`
	Version string `json:"version" yaml:"version"`
}
type Config struct {
	SchemaVersion int        `json:"schema_version" yaml:"schema_version"`
	Name          string     `json:"name" yaml:"name"`
	Model         string     `json:"model" yaml:"model"`
	Skills        []SkillRef `json:"skills" yaml:"skills"`
	Tools         []string   `json:"tools" yaml:"tools"`
	Context       struct {
		MaxInputTokens       int    `json:"max_input_tokens" yaml:"max_input_tokens"`
		ReservedOutputTokens int    `json:"reserved_output_tokens" yaml:"reserved_output_tokens"`
		Compression          string `json:"compression" yaml:"compression"`
	} `json:"context" yaml:"context"`
	Limits struct {
		MaxSteps       int `json:"max_steps" yaml:"max_steps"`
		TimeoutSeconds int `json:"timeout_seconds" yaml:"timeout_seconds"`
	} `json:"limits" yaml:"limits"`
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
	CatalogPublish(context.Context, string, string, Version) error
	CatalogVersions(context.Context, string, string, string, string, bool, identity.Query) ([]Version, error)
	CatalogVersion(context.Context, string, string, string, string, string) (Version, error)
	CatalogReferences(context.Context, string, string, string, string, string, identity.Query) ([]Reference, error)
	CatalogState(context.Context, string, string, string, string, string, string, bool) error
}
