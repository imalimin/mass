package resource

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type WorkspaceDir interface {
	Root() *os.Root
	Pack(context.Context) (io.ReadCloser, storage.Manifest, error)
	Restore(context.Context, io.Reader, storage.Manifest) error
	Clear() error
}
type Workspaces interface {
	With(context.Context, string, string, func(WorkspaceDir) error) error
}
type SnapshotRepository interface {
	Snapshot(context.Context, string, string, string) (storage.Snapshot, error)
	SnapshotBase(context.Context, string, string, string, string) (string, error)
	SnapshotEvent(context.Context, string, string, string, string) (storage.Snapshot, bool, error)
}
type SnapshotStore struct {
	Repo       SnapshotRepository
	Content    *ContentStore
	Journal    *journal.Journal
	Workspaces Workspaces
	Timeout    time.Duration
	ActorKind  string
}

// SaveRequest IDs and timestamp must be retained across unknown-commit retries.
// The caller must already have stopped all I/O before invoking Save.
type SaveRequest struct {
	SpaceID, SessionID, RunID, SnapshotID, ArchiveID, ManifestID, EventID, AppendKey string
	OccurredAt                                                                       time.Time
	Finish                                                                           *journal.Proposed
}

func (s *SnapshotStore) Save(ctx context.Context, actor string, r SaveRequest) (v storage.Snapshot, err error) {
	for _, id := range []string{r.SpaceID, r.SessionID, r.RunID, r.SnapshotID, r.ArchiveID, r.ManifestID, r.EventID} {
		if !storage.ValidID(id) {
			return v, storage.ErrInvalid
		}
	}
	if r.OccurredAt.IsZero() {
		return v, storage.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	// Reconcile a possibly committed result before touching the mutable directory.
	old, found, err := s.Repo.SnapshotEvent(ctx, actor, r.SpaceID, r.SessionID, r.EventID)
	if err != nil {
		return v, err
	}
	if found {
		if old.ID != r.SnapshotID || old.SourceRunID != r.RunID || old.ObjectID != r.ArchiveID || old.ManifestRef != r.ManifestID {
			return v, storage.ErrConflict
		}
		return s.commit(ctx, actor, r, old)
	}
	err = s.Workspaces.With(ctx, r.SpaceID, r.SessionID, func(dir WorkspaceDir) error {
		base, e := s.Repo.SnapshotBase(ctx, actor, r.SpaceID, r.SessionID, r.RunID)
		if e != nil {
			return e
		}
		archive, manifest, e := dir.Pack(ctx)
		if e != nil {
			return e
		}
		defer archive.Close()
		if s.Journal != nil {
			history, _, err := History(ctx, s.Journal, actor, r.SpaceID, r.SessionID)
			if err != nil {
				return err
			}
			var plan *run.Plan
			artifacts := []run.Artifact{}
			through := int64(0)
			for _, event := range history {
				if event.RunID != r.RunID {
					continue
				}
				switch event.Type {
				case "plan.updated":
					var p run.Plan
					journal.Decode(event.Data, &p)
					plan = &p
					through = event.Sequence
				case "artifact.published":
					var a run.Artifact
					journal.Decode(event.Data, &a)
					artifacts = append(artifacts, a)
					through = event.Sequence
				case "tool.execution.completed":
					through = event.Sequence
				}
			}
			if through > 0 {
				manifest.State, _ = json.Marshal(map[string]any{"run_id": r.RunID, "through_sequence": through, "plan": plan, "artifacts": artifacts})
			}
		}
		a, e := s.Content.Put(ctx, actor, r.SpaceID, r.ArchiveID, "workspace.archive", "application/gzip", archive)
		if e != nil {
			return e
		}
		raw, e := json.Marshal(manifest)
		if e != nil {
			return e
		}
		if len(raw) > 4<<20 {
			return storage.ErrWorkspace
		}
		m, e := s.Content.Put(ctx, actor, r.SpaceID, r.ManifestID, "workspace.manifest", "application/json", bytes.NewReader(raw))
		if e != nil {
			return e
		}
		v = storage.Snapshot{ID: r.SnapshotID, SpaceID: r.SpaceID, SessionID: r.SessionID, SourceRunID: r.RunID, BaseSnapshotID: base, ObjectID: a.ID, ManifestRef: m.ID, URI: a.URI, SHA256: a.SHA256, FormatVersion: 1}
		v, e = s.commit(ctx, actor, r, v)
		return e
	})
	return
}
func (s *SnapshotStore) commit(ctx context.Context, actor string, r SaveRequest, v storage.Snapshot) (storage.Snapshot, error) {
	a, err := s.Content.Repo.Object(ctx, actor, r.SpaceID, v.ObjectID, false)
	if err != nil {
		return v, err
	}
	m, err := s.Content.Repo.Object(ctx, actor, r.SpaceID, v.ManifestRef, false)
	if err != nil {
		return v, err
	}
	raw, _ := json.Marshal(v)
	kind := s.ActorKind
	if kind == "" {
		kind = "user"
	}
	events := []journal.Proposed{{EventID: r.EventID, RunID: r.RunID, Type: "workspace.snapshot.created", SchemaVersion: 1, Actor: journal.Actor{Kind: kind, ID: actor}, OccurredAt: r.OccurredAt, Data: raw, Refs: []storage.Ref{{Role: "archive", ObjectID: a.ID, SHA256: a.SHA256}, {Role: "manifest", ObjectID: m.ID, SHA256: m.SHA256}}}}
	if r.Finish != nil {
		if r.Finish.Type != "run.finished" || r.Finish.RunID != r.RunID {
			return v, storage.ErrInvalid
		}
		events = append(events, *r.Finish)
	}
	_, err = s.Journal.Append(ctx, actor, journal.Request{SpaceID: r.SpaceID, SessionID: r.SessionID, AppendKey: r.AppendKey, Events: events})
	return v, err
}

// Restore loads the target session's committed pointer, including a snapshot
// selected in session.created. It never inherits source conversation events.
func (s *SnapshotStore) Restore(ctx context.Context, actor, space, session string) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	return s.Workspaces.With(ctx, space, session, func(dir WorkspaceDir) error {
		id, err := s.Repo.SnapshotBase(ctx, actor, space, session, "")
		if err != nil {
			return err
		}
		manifest := storage.Manifest{FormatVersion: 1, Entries: []storage.Entry{}}
		var reader io.Reader = bytes.NewReader(nil)
		if id != "" {
			v, err := s.Repo.Snapshot(ctx, actor, space, id)
			if err != nil {
				return err
			}
			_, mf, err := s.Content.Read(ctx, actor, space, v.ManifestRef)
			if err != nil {
				return err
			}
			defer mf.Close()
			// Manifest has its own bounded parse, independent of the archive size limit.
			raw, err := io.ReadAll(io.LimitReader(mf, 4<<20+1))
			if err != nil || len(raw) > 4<<20 {
				return storage.ErrIntegrity
			}
			if err = journal.Decode(raw, &manifest); err != nil {
				return err
			}
			object, f, err := s.Content.Read(ctx, actor, space, v.ObjectID)
			if err != nil {
				return err
			}
			defer f.Close()
			if object.SHA256 != v.SHA256 {
				return storage.ErrIntegrity
			}
			reader = f
		}
		if id == "" { // Pack an empty directory through the same archive contract.
			f, m, err := dir.Pack(ctx)
			if err != nil {
				return err
			}
			defer f.Close()
			if len(m.Entries) > 0 {
				return storage.ErrWorkspace
			}
			reader = f
		}
		if err = dir.Restore(ctx, reader, manifest); err != nil {
			return err
		}
		raw, _ := json.Marshal(journal.RestoreData{SnapshotID: id, Reason: "explicit_restore"})
		eventID := uuid.NewString()
		_, err = s.Journal.Append(ctx, actor, journal.Request{SpaceID: space, SessionID: session, AppendKey: eventID, Events: []journal.Proposed{{EventID: eventID, Type: "workspace.restored", SchemaVersion: 1, Actor: journal.Actor{Kind: "user", ID: actor}, OccurredAt: time.Now(), Data: raw}}})
		if err != nil {
			return errors.Join(err, dir.Clear())
		}
		return nil
	})
}
