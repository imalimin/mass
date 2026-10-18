package storage

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/google/uuid"
)

var (
	ErrConflict  = errors.New("event_conflict")
	ErrSchema    = errors.New("unsupported_event_schema")
	ErrInvalid   = errors.New("invalid_storage_request")
	ErrIntegrity = errors.New("content_unavailable")
	ErrWorkspace = errors.New("workspace_unavailable")
	ErrActive    = errors.New("run_active")
)

func ValidID(id string) bool { u, e := uuid.Parse(id); return e == nil && u.String() == id }

type Object struct {
	SpaceID   string `json:"space_id"`
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Key       string `json:"-"`
	URI       string `json:"tos_uri"`
	ETag      string `json:"-"`
	VersionID string `json:"-"`
	State     string `json:"state"`
}
type BlobInfo struct{ ETag, VersionID string }
type BlobStore interface {
	PutNew(context.Context, string, io.Reader, int64, string) (BlobInfo, error)
	Open(context.Context, string, string) (io.ReadCloser, error)
	URI(string) string
	Check(context.Context) error
}
type Ref struct {
	Role     string `json:"role"`
	Ordinal  int    `json:"ordinal"`
	ObjectID string `json:"object_id"`
	SHA256   string `json:"sha256"`
}
type Entry struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}
type Manifest struct {
	FormatVersion int     `json:"format_version"`
	Entries       []Entry `json:"entries"`
}
type Snapshot struct {
	ID             string `json:"id"`
	SpaceID        string `json:"space_id"`
	SessionID      string `json:"session_id"`
	SourceRunID    string `json:"source_run_id"`
	BaseSnapshotID string `json:"base_snapshot_id,omitempty"`
	ObjectID       string `json:"object_id"`
	ManifestRef    string `json:"manifest_ref"`
	URI            string `json:"tos_uri"`
	SHA256         string `json:"archive_sha256"`
	FormatVersion  int    `json:"format_version"`
}

// ContextReader ensures bounded copy loops react to cancellation between reads.
type ContextReader struct {
	Context context.Context
	Reader  io.Reader
}

func (r ContextReader) Read(p []byte) (int, error) {
	if err := r.Context.Err(); err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
	MaxEntries    int
	Timeout       time.Duration
}
