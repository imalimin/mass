package resource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"os"
	"path"
	"regexp"
	"strings"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type ContentRepository interface {
	Object(context.Context, string, string, string, bool) (storage.Object, error)
	BeginObject(context.Context, string, storage.Object) (storage.Object, error)
	ReadyObject(context.Context, string, storage.Object) error
}
type ContentStore struct {
	Repo     ContentRepository
	Blobs    storage.BlobStore
	Prefix   string
	MaxBytes int64
	Inline   InlineContent
}
type InlineContent interface {
	StoreInline(context.Context, storage.Object, []byte) error
	OpenInline(context.Context, string, string, string) (storage.Object, io.ReadCloser, error)
}
type File struct{ *os.File }

func (f *File) Close() error {
	name := f.Name()
	err := f.File.Close()
	remove := os.Remove(name)
	return errors.Join(err, remove)
}

var kindPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func (c *ContentStore) Put(ctx context.Context, actor, space, id, kind, media string, source io.Reader) (storage.Object, error) {
	key := strings.TrimSuffix(c.Prefix, "/") + "/" + space + "/objects/" + id
	return c.PutAt(ctx, actor, space, id, kind, media, key, source)
}

func (c *ContentStore) CatalogObjectKey(space, kind, definition, filename string) string {
	return path.Join(c.Prefix, space, kind+"s", definition, filename)
}

func (c *ContentStore) PutAt(ctx context.Context, actor, space, id, kind, media, key string, source io.Reader) (storage.Object, error) {
	if !storage.ValidShortID(space) || !storage.ValidID(id) || !kindPattern.MatchString(kind) || c.MaxBytes < 1 {
		return storage.Object{}, storage.ErrInvalid
	}
	if _, _, err := mime.ParseMediaType(media); err != nil || len(media) > 128 {
		return storage.Object{}, storage.ErrInvalid
	}
	if c.Inline != nil && inlineKind(kind) {
		raw, err := io.ReadAll(io.LimitReader(storage.ContextReader{Context: ctx, Reader: source}, c.MaxBytes+1))
		if err != nil {
			return storage.Object{}, err
		}
		if int64(len(raw)) > c.MaxBytes {
			return storage.Object{}, storage.ErrInvalid
		}
		hash := sha256.Sum256(raw)
		object := storage.Object{SpaceID: space, ID: id, Kind: kind, MediaType: media, Size: int64(len(raw)), SHA256: hex.EncodeToString(hash[:]), State: "ready"}
		return object, c.Inline.StoreInline(ctx, object, raw)
	}
	file, err := os.CreateTemp("", "mass-content-*")
	if err != nil {
		return storage.Object{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, hash), io.LimitReader(storage.ContextReader{Context: ctx, Reader: source}, c.MaxBytes+1))
	if err != nil {
		return storage.Object{}, err
	}
	if n > c.MaxBytes {
		return storage.Object{}, storage.ErrInvalid
	}
	object := storage.Object{SpaceID: space, ID: id, Kind: kind, MediaType: media, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil)), Key: key, URI: c.Blobs.URI(key), State: "uploading"}
	object, err = c.Repo.BeginObject(ctx, actor, object)
	if err != nil {
		return object, err
	}
	if object.State != "ready" {
		if _, err = file.Seek(0, 0); err != nil {
			return object, err
		}
		info, putErr := c.Blobs.PutNew(ctx, key, storage.ContextReader{Context: ctx, Reader: file}, n, media)
		// Read-back also resolves a lost upload response or a ForbidOverwrite retry.
		// A failed/mismatched read never creates a ready reference.
		verified, verifyErr := c.verify(ctx, object)
		if verifyErr != nil {
			return object, errors.Join(putErr, verifyErr)
		}
		verified.Close()
		object.ETag = info.ETag
		object.VersionID = info.VersionID
		if err = c.Repo.ReadyObject(ctx, actor, object); err != nil {
			return object, err
		}
		object.State = "ready"
	} else {
		file, err := c.verify(ctx, object)
		if err != nil {
			return object, err
		}
		file.Close()
	}
	return object, nil
}

func (c *ContentStore) inlineBytes(ctx context.Context, actor, space, id string, limit int64) ([]byte, error) {
	if c.Inline == nil {
		return nil, storage.ErrBlobNotFound
	}
	o, r, err := c.Inline.OpenInline(ctx, actor, space, id)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if o.Size > limit {
		return nil, run.ErrOverflow
	}
	return io.ReadAll(storage.ContextReader{Context: ctx, Reader: r})
}

func inlineKind(kind string) bool {
	switch kind {
	case "session.definition", "user.message", "model.response", "context.projection", "context.summary", "workspace.manifest":
		return true
	}
	return false
}

func (c *ContentStore) verify(ctx context.Context, o storage.Object) (*File, error) {
	if o.Size < 0 || o.Size > c.MaxBytes {
		return nil, storage.ErrIntegrity
	}
	reader, err := c.Blobs.Open(ctx, o.Key, o.VersionID)
	if err != nil {
		return nil, errors.Join(storage.ErrIntegrity, err)
	}
	defer reader.Close()
	return c.verifiedFile(ctx, o, reader)
}

func (c *ContentStore) verifiedFile(ctx context.Context, o storage.Object, reader io.Reader) (*File, error) {
	f, err := os.CreateTemp("", "mass-verified-*")
	if err != nil {
		return nil, err
	}
	result := &File{f}
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(storage.ContextReader{Context: ctx, Reader: reader}, o.Size+1))
	if err != nil || n != o.Size || hex.EncodeToString(hash.Sum(nil)) != o.SHA256 {
		result.Close()
		return nil, errors.Join(storage.ErrIntegrity, err)
	}
	if _, err = f.Seek(0, 0); err != nil {
		result.Close()
		return nil, err
	}
	return result, nil
}

// Read exposes only ready objects already referenced by committed facts. An
// upload that succeeded before a failed journal transaction stays invisible.
func (c *ContentStore) Read(ctx context.Context, actor, space, id string) (storage.Object, *File, error) {
	if c.Inline != nil {
		o, reader, err := c.Inline.OpenInline(ctx, actor, space, id)
		if err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
			return o, nil, err
		}
		if reader != nil {
			defer reader.Close()
			f, err := c.verifiedFile(ctx, o, reader)
			return o, f, err
		}
	}
	o, err := c.Repo.Object(ctx, actor, space, id, true)
	if err != nil {
		return o, nil, err
	}
	f, err := c.verify(ctx, o)
	return o, f, err
}

func (c *ContentStore) Object(ctx context.Context, actor, space, id string) (storage.Object, error) {
	if c.Inline != nil {
		object, reader, err := c.Inline.OpenInline(ctx, actor, space, id)
		if reader != nil {
			reader.Close()
		}
		if !errors.Is(err, storage.ErrBlobNotFound) {
			return object, err
		}
	}
	return c.Repo.Object(ctx, actor, space, id, true)
}
