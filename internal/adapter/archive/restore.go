package archive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/sessionlog"
)

type CopyObject func(context.Context, storage.Object) (storage.Object, error)

// CopyCatalog preserves immutable Agent and Skill objects and rewrites only
// the destination index once those objects have been verified.
func (c *Catalog) CopyCatalog(ctx context.Context, target storage.BlobStore, prefix, space string, copyObject CopyObject) (int, error) {
	source := Key(c.Prefix, space, "catalog.json")
	destination := Key(prefix, space, "catalog.json")
	var index catalogIndex
	if err := readJSON(ctx, c.Blobs, source, &index); err != nil {
		if !errors.Is(err, storage.ErrBlobNotFound) {
			return 0, err
		}
		if err := readJSON(ctx, target, destination, &index); err != nil {
			if errors.Is(err, storage.ErrBlobNotFound) {
				return 0, nil
			}
			return 0, err
		}
	}
	copied := map[string]Ref{}
	copyRef := func(ref Ref) (Ref, error) {
		if v, ok := copied[ref.ID]; ok {
			return v, nil
		}
		next, err := copyObject(ctx, ref.Metadata())
		if err != nil {
			return Ref{}, err
		}
		result := Reference(next)
		copied[ref.ID] = result
		return result, nil
	}
	for _, d := range index {
		var err error
		if d.Draft, err = copyRef(d.Draft); err != nil {
			return 0, err
		}
		for _, v := range d.Versions {
			if v.Object, err = copyRef(v.Object); err != nil {
				return 0, err
			}
		}
	}
	raw, err := json.Marshal(index)
	if err != nil {
		return 0, err
	}
	_, err = target.Overwrite(ctx, destination, bytes.NewReader(raw), int64(len(raw)), "application/json")
	return len(copied) + 1, err
}

func (r *Runtime) CopySession(ctx context.Context, target storage.BlobStore, prefix, space, session string, copyObject CopyObject) (int, error) {
	lines, err := r.Log.ReadRemote(ctx, space, session)
	origin := r
	if errors.Is(err, storage.ErrBlobNotFound) {
		origin = &Runtime{Log: &sessionlog.Store{Blobs: target, Prefix: prefix}}
		lines, err = origin.Log.ReadRemote(ctx, space, session)
	}
	if err != nil {
		return 0, err
	}
	s := newConversation()
	if err := origin.rehydrate(s, lines); err != nil {
		return 0, err
	}
	if s.session.SpaceID != space || s.session.ID != session || s.active != "" {
		return 0, storage.ErrIntegrity
	}
	for _, ref := range s.blobs {
		if _, err := copyObject(ctx, ref.Metadata()); err != nil {
			return 0, err
		}
	}
	if len(s.draftPackages) > 0 {
		var created sessionlog.Created
		if err := json.Unmarshal(lines[0], &created); err != nil {
			return 0, err
		}
		for i, ref := range created.DraftSkillPackages {
			object, err := copyObject(ctx, s.draftPackages[ref.ID].Metadata())
			if err != nil {
				return 0, err
			}
			created.DraftSkillPackages[i].Object, created.DraftSkillPackages[i].Key = object, object.Key
		}
		line, err := json.Marshal(created)
		if err != nil {
			return 0, err
		}
		lines[0] = line
	}
	var body strings.Builder
	for _, line := range lines {
		body.Write(line)
		body.WriteByte('\n')
	}
	key := Key(prefix, space, "sessions", session, "session.jsonl")
	_, err = target.Overwrite(ctx, key, strings.NewReader(body.String()), int64(body.Len()), "application/x-ndjson")
	return len(s.blobs) + len(s.draftPackages) + 1, err
}
