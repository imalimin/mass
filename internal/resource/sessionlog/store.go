package sessionlog

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/imalimin/mass/internal/domain/storage"
)

type Store struct {
	Root   string
	Prefix string
	Blobs  storage.BlobStore
}

func Key(prefix, space, session string) string {
	key := space + "/sessions/" + session + "/session.jsonl"
	if prefix == "" {
		return key
	}
	return strings.TrimSuffix(prefix, "/") + "/" + key
}

func (s *Store) paths(space, session string) (local, remote string, err error) {
	if !storage.ValidShortID(space) || !storage.ValidShortID(session) {
		return "", "", storage.ErrInvalid
	}
	key := Key(s.Prefix, space, session)
	return filepath.Join(s.Root, filepath.FromSlash(key)), key, nil
}

func (s *Store) Read(ctx context.Context, space, session string) ([]json.RawMessage, error) {
	local, remote, err := s.paths(space, session)
	if err != nil {
		return nil, err
	}
	var r io.ReadCloser
	r, err = os.Open(local)
	if errors.Is(err, os.ErrNotExist) {
		r, err = s.Blobs.Open(ctx, remote, "")
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return read(ctx, r)
}

func (s *Store) ReadRemote(ctx context.Context, space, session string) ([]json.RawMessage, error) {
	_, key, err := s.paths(space, session)
	if err != nil {
		return nil, err
	}
	r, err := s.Blobs.Open(ctx, key, "")
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return read(ctx, r)
}

func read(ctx context.Context, r io.Reader) ([]json.RawMessage, error) {
	var records []json.RawMessage
	in := bufio.NewScanner(r)
	in.Buffer(make([]byte, 32<<10), 128<<20)
	for in.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var h Header
		if json.Unmarshal(in.Bytes(), &h) != nil || !h.valid() || h.Seq != int64(len(records))+1 ||
			(h.Type == SessionCreated) != (h.Seq == 1) {
			return nil, storage.ErrIntegrity
		}
		records = append(records, append(json.RawMessage(nil), in.Bytes()...))
	}
	if err := in.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, storage.ErrIntegrity
	}
	return records, nil
}

// Save records a complete replacement locally. The caller serializes edits to
// one session and retries Upload with the same local file after network errors.
func (s *Store) Save(ctx context.Context, space, session string, previous []json.RawMessage, entries ...any) error {
	local, _, err := s.paths(space, session)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(local), 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(local), ".session-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	out := bufio.NewWriter(tmp)
	for _, raw := range previous {
		if err := write(ctx, out, raw); err != nil {
			return err
		}
	}
	for i, entry := range entries {
		raw, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		var h Header
		if json.Unmarshal(raw, &h) != nil || !h.valid() || h.Seq != int64(len(previous)+i+1) ||
			(h.Type == SessionCreated) != (h.Seq == 1) {
			return fmt.Errorf("%w: session record %d", storage.ErrInvalid, len(previous)+i+1)
		}
		if err := write(ctx, out, raw); err != nil {
			return err
		}
	}
	if err := out.Flush(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), local)
}

func write(ctx context.Context, w *bufio.Writer, raw json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := io.Copy(w, bytes.NewReader(raw)); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

func (s *Store) Upload(ctx context.Context, space, session string) error {
	local, remote, err := s.paths(space, session)
	if err != nil {
		return err
	}
	f, err := os.Open(local)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = s.Blobs.Overwrite(ctx, remote, f, info.Size(), "application/x-ndjson")
	return err
}

// Diagnostic records omit storage locators and the request deduplication key.
func Diagnostic(records []json.RawMessage, runID string) ([]json.RawMessage, error) {
	return diagnostic(records, func(id string) bool { return runID == "" || id == runID })
}

// DiagnosticForRuns excludes all records outside the supplied Run IDs.
func DiagnosticForRuns(records []json.RawMessage, runIDs []string) ([]json.RawMessage, error) {
	return diagnostic(records, func(id string) bool { return slices.Contains(runIDs, id) })
}

func diagnostic(records []json.RawMessage, include func(string) bool) ([]json.RawMessage, error) {
	out := []json.RawMessage{}
	for _, raw := range records {
		var h Header
		if json.Unmarshal(raw, &h) != nil {
			return nil, storage.ErrIntegrity
		}
		if !include(h.RunID) {
			continue
		}
		switch h.Type {
		case RunAccepted:
			var value map[string]json.RawMessage
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			delete(value, "request_key")
			delete(value, "request_digest")
			delete(value, "base_snapshot_id")
			raw, _ = json.Marshal(value)
		case RunFinished:
			var value map[string]json.RawMessage
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			present := len(value["workspace_snapshot"]) > 0 && string(value["workspace_snapshot"]) != "null"
			delete(value, "workspace_snapshot")
			value["workspace_snapshot_committed"], _ = json.Marshal(present)
			raw, _ = json.Marshal(value)
		case WorkspaceRestored:
			var value map[string]json.RawMessage
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, err
			}
			delete(value, "snapshot_id")
			raw, _ = json.Marshal(value)
		}
		out = append(out, raw)
	}
	return out, nil
}
