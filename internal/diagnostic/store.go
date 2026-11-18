// Package diagnostic persists session-scoped operational logs separately from replay data.
package diagnostic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
)

const MediaType = "application/x-ndjson"
const batchBytes = 64 << 10

type Objects interface {
	Open(context.Context, string, string) (io.ReadCloser, error)
	Size(context.Context, string) (int64, error)
	OpenRange(context.Context, string, int64, int64) (io.ReadCloser, error)
	Append(context.Context, string, int64, []byte, string) (int64, error)
}

type Entry struct {
	Timestamp time.Time `json:"timestamp"`
	Level     string    `json:"level"`
	Event     string    `json:"event"`
	RunID     *string   `json:"run_id"`
	Message   string    `json:"msg"`
}

type Store struct {
	root, prefix string
	objects      Objects
	logger       *slog.Logger
	mu           sync.Mutex
	pending      map[string]uint64
	wake         chan struct{}
}

// uploadState describes a range in the local file. Remote is the target
// offset for that range, or the last confirmed remote end when End == Local.
type uploadState struct {
	Local    int64 `json:"local"`
	End      int64 `json:"end"`
	Remote   int64 `json:"remote"`
	Conflict bool  `json:"conflict,omitempty"`
}

func NewStore(root, prefix string, objects Objects, logger *slog.Logger) *Store {
	return &Store{root: root, prefix: prefix, objects: objects, logger: logger, pending: map[string]uint64{}, wake: make(chan struct{}, 1)}
}

func (s *Store) key(space, session string) string {
	return path.Join(s.prefix, space, "sessions", session, session+".jsonl")
}

func (s *Store) Open(ctx context.Context, space, session string) (io.ReadCloser, error) {
	if !storage.ValidShortID(space) || !storage.ValidShortID(session) {
		return nil, storage.ErrInvalid
	}
	return s.objects.Open(ctx, s.key(space, session), "")
}

func (s *Store) Write(space, session, runID, level, event, msg string) {
	if !storage.ValidShortID(space) || !storage.ValidShortID(session) {
		return
	}
	entry := Entry{Timestamp: time.Now().UTC(), Level: level, Event: event, Message: msg}
	if runID != "" {
		entry.RunID = &runID
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	key := s.key(space, session)
	s.mu.Lock()
	err = appendLocal(s.local(key), append(data, '\n'))
	if err == nil {
		s.pending[key]++
	}
	s.mu.Unlock()
	if err != nil {
		s.warn("diagnostic log local append failed", key, err)
		return
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) local(key string) string { return filepath.Join(s.root, filepath.FromSlash(key)) }

func appendLocal(name string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	// A crash during a write can leave a partial final line. Discard that
	// tail before extending the file, so it cannot become an uploaded row.
	originalEnd := end
	var tail [4096]byte
	for end > 0 {
		n := min(end, int64(len(tail)))
		if _, err := f.ReadAt(tail[:n], end-n); err != nil {
			return err
		}
		if i := bytes.LastIndexByte(tail[:n], '\n'); i >= 0 {
			end = end - n + int64(i) + 1
			break
		}
		end -= n
	}
	if end != originalEnd {
		if err := f.Truncate(end); err != nil {
			return err
		}
	}
	if _, err := f.WriteAt(data, end); err != nil {
		return errors.Join(err, f.Truncate(end))
	}
	if err := f.Sync(); err != nil {
		return errors.Join(err, f.Truncate(end))
	}
	return nil
}

func (s *Store) Run(ctx context.Context, check func(context.Context) error) {
	s.scan()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	// Cached offsets belong to this uploader only. Restart checks TOS before
	// sending the first new batch for each object.
	offsets := map[string]int64{}
	for {
		s.drain(ctx, check, offsets)
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
	}
}

func (s *Store) scan() {
	root := s.local(s.prefix)
	err := filepath.WalkDir(root, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, name)
		if err != nil || rel == "." {
			return err
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if d.IsDir() {
			if len(parts) > 3 || !storage.ValidShortID(parts[0]) || len(parts) >= 2 && parts[1] != "sessions" || len(parts) == 3 && !storage.ValidShortID(parts[2]) {
				return fs.SkipDir
			}
		} else if d.Type().IsRegular() && len(parts) == 4 && parts[3] == parts[2]+".jsonl" {
			s.mu.Lock()
			s.pending[s.key(parts[0], parts[2])]++
			s.mu.Unlock()
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.warn("diagnostic log queue scan failed", "", err)
	}
}

func (s *Store) drain(ctx context.Context, check func(context.Context) error, offsets map[string]int64) {
	s.mu.Lock()
	pending := make(map[string]uint64, len(s.pending))
	for key, version := range s.pending {
		pending[key] = version
	}
	s.mu.Unlock()
	for key, version := range pending {
		if ctx.Err() != nil {
			return
		}
		clean, err := s.flush(ctx, check, offsets, key)
		if err != nil && ctx.Err() == nil {
			s.warn("diagnostic log upload pending", key, err)
		}
		if clean {
			s.mu.Lock()
			if s.pending[key] == version {
				delete(s.pending, key)
			}
			s.mu.Unlock()
		}
	}
}

func (s *Store) flush(ctx context.Context, check func(context.Context) error, offsets map[string]int64, key string) (bool, error) {
	name := s.local(key)
	state, err := loadState(name)
	if err != nil || state.Conflict {
		return state.Conflict, err
	}
	for ctx.Err() == nil {
		s.mu.Lock()
		data, err := pendingBytes(name, state)
		s.mu.Unlock()
		if err != nil || len(data) == 0 {
			return err == nil, err
		}
		if err := check(ctx); err != nil {
			return false, err
		}
		if state.End > state.Local {
			remote, already, err := s.reconcile(ctx, key, state.Remote, data)
			if errors.Is(err, storage.ErrConflict) {
				state.Conflict = true
				saveErr := saveState(name, state)
				return saveErr == nil, errors.Join(err, saveErr)
			}
			if err != nil {
				return false, err
			}
			if already {
				if err := confirm(name, &state, remote); err != nil {
					return false, err
				}
				offsets[key] = remote
				continue
			}
		} else {
			remote, known := offsets[key]
			if !known {
				remote, err = s.size(ctx, key)
				if err != nil {
					return false, err
				}
				if remote < state.Remote {
					state.Conflict = true
					saveErr := saveState(name, state)
					return saveErr == nil, errors.Join(storage.ErrConflict, saveErr)
				}
			}
			state.Remote, state.End = remote, state.Local+int64(len(data))
			if err := saveState(name, state); err != nil {
				return false, err
			}
		}
		next, err := s.objects.Append(ctx, key, state.Remote, data, MediaType)
		if err != nil {
			return false, err
		}
		if next != state.Remote+int64(len(data)) {
			return false, errors.New("TOS returned an unexpected diagnostic append offset")
		}
		if err := confirm(name, &state, next); err != nil {
			return false, err
		}
		offsets[key] = next
	}
	return false, ctx.Err()
}

func (s *Store) size(ctx context.Context, key string) (int64, error) {
	size, err := s.objects.Size(ctx, key)
	if errors.Is(err, storage.ErrBlobNotFound) {
		return 0, nil
	}
	return size, err
}

func (s *Store) reconcile(ctx context.Context, key string, offset int64, data []byte) (remote int64, already bool, err error) {
	remote, err = s.size(ctx, key)
	if err != nil || remote == offset {
		return remote, false, err
	}
	size := int64(len(data))
	if remote < offset+size {
		return remote, false, storage.ErrConflict
	}
	f, err := s.objects.OpenRange(ctx, key, offset, size)
	if err != nil {
		return remote, false, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return remote, false, err
	}
	if !bytes.Equal(raw, data) {
		return remote, false, storage.ErrConflict
	}
	return remote, true, nil
}

func pendingBytes(name string, state uploadState) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if state.End > state.Local {
		data := make([]byte, state.End-state.Local)
		_, err := f.ReadAt(data, state.Local)
		return data, err
	}
	data := make([]byte, batchBytes)
	n, err := f.ReadAt(data, state.Local)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data[:bytes.LastIndexByte(data[:n], '\n')+1], nil
}

func loadState(name string) (state uploadState, err error) {
	raw, err := os.ReadFile(name + ".upload")
	if errors.Is(err, fs.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil || state.Local < 0 || state.End < state.Local || state.End-state.Local > batchBytes || state.Remote < 0 {
		return uploadState{}, storage.ErrIntegrity
	}
	return state, nil
}

func saveState(name string, state uploadState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(name), ".diagnostic-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(raw); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), name+".upload"); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func confirm(name string, state *uploadState, next int64) error {
	state.Local, state.Remote = state.End, next
	return saveState(name, *state)
}

func (s *Store) warn(msg, key string, err error) {
	s.logger.Warn(msg, "object", key, "cause", Cause(err))
}

// Copy streams only records matching runID, or the entire log for a session.
func Copy(ctx context.Context, dst io.Writer, src io.Reader, runID string) error {
	scan := bufio.NewScanner(src)
	scan.Buffer(make([]byte, 4096), batchBytes)
	for scan.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := scan.Bytes()
		if runID != "" {
			var entry Entry
			if err := json.Unmarshal(raw, &entry); err != nil {
				return storage.ErrIntegrity
			}
			if entry.RunID == nil || *entry.RunID != runID {
				continue
			}
		}
		if _, err := dst.Write(append(raw, '\n')); err != nil {
			return err
		}
	}
	return scan.Err()
}
