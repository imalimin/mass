package diagnostic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/imalimin/mass/internal/domain/storage"
)

const (
	spaceID   = "aaaaaaaaaaaaaaaa"
	sessionID = "bbbbbbbbbbbbbbbb"
	runID     = "cccccccccccccccc"
)

type memoryObject struct {
	data       []byte
	loseReply  bool
	rangeReads int
	appends    []int64
}

func (o *memoryObject) Open(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(o.data)), nil
}

func (o *memoryObject) Size(context.Context, string) (int64, error) {
	if o.data == nil {
		return 0, storage.ErrBlobNotFound
	}
	return int64(len(o.data)), nil
}

func (o *memoryObject) OpenRange(_ context.Context, _ string, start, size int64) (io.ReadCloser, error) {
	o.rangeReads++
	return io.NopCloser(bytes.NewReader(o.data[start : start+size])), nil
}

func (o *memoryObject) Append(_ context.Context, _ string, offset int64, data []byte, media string) (int64, error) {
	if offset != int64(len(o.data)) || media != MediaType {
		return 0, storage.ErrConflict
	}
	o.appends = append(o.appends, offset)
	o.data = append(o.data, data...)
	if o.loseReply {
		o.loseReply = false
		return 0, io.ErrUnexpectedEOF
	}
	return int64(len(o.data)), nil
}

func TestUploadResumesAfterLostReplyAndHostChange(t *testing.T) {
	ctx := context.Background()
	check := func(context.Context) error { return nil }
	remote := &memoryObject{loseReply: true}
	root := t.TempDir()
	newStore := func(root string) *Store { return NewStore(root, "mass", remote, slog.New(slog.DiscardHandler)) }
	store := newStore(root)
	key := store.key(spaceID, sessionID)
	store.Write(spaceID, sessionID, runID, "info", "run.claimed", "first")
	if clean, err := store.flush(ctx, check, map[string]int64{}, key); clean || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("lost reply: clean=%t err=%v", clean, err)
	}
	first := bytes.Clone(remote.data)

	store = newStore(root)
	store.scan()
	if len(store.pending) != 1 {
		t.Fatal("restart did not discover local diagnostic queue")
	}
	store.drain(ctx, check, map[string]int64{})
	if len(store.pending) != 0 || remote.rangeReads != 1 || len(remote.appends) != 1 || !bytes.Equal(remote.data, first) {
		t.Fatal("restart did not acknowledge the original batch without appending it again")
	}

	store = newStore(t.TempDir())
	store.Write(spaceID, sessionID, "", "warn", "workspace.operation_failed", "second")
	store.drain(ctx, check, map[string]int64{})
	if len(remote.appends) != 2 || remote.appends[1] != int64(len(first)) || !bytes.HasPrefix(remote.data, first) {
		t.Fatal("new host did not append at the remote end", remote.appends)
	}
	var filtered bytes.Buffer
	if err := Copy(ctx, &filtered, bytes.NewReader(remote.data), runID); err != nil || !bytes.Equal(filtered.Bytes(), first) {
		t.Fatalf("run selection: %q, %v", filtered.Bytes(), err)
	}
	for i, line := range bytes.Split(bytes.TrimSuffix(remote.data, []byte{'\n'}), []byte{'\n'}) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil || len(fields) != 5 {
			t.Fatalf("invalid row: %s (%v)", line, err)
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil || entry.Timestamp.IsZero() || entry.Timestamp.Location() != time.UTC || entry.Event == "" || entry.Level == "" || entry.Message == "" {
			t.Fatalf("invalid fields: %s (%v)", line, err)
		}
		if i == 0 && (entry.RunID == nil || *entry.RunID != runID) || i == 1 && string(fields["run_id"]) != "null" {
			t.Fatalf("wrong diagnostic scope: %s", line)
		}
	}
}

func TestUploadPausesOnConflictingRemoteBytes(t *testing.T) {
	ctx := context.Background()
	check := func(context.Context) error { return nil }
	remote := &memoryObject{loseReply: true}
	store := NewStore(t.TempDir(), "", remote, slog.New(slog.DiscardHandler))
	key := store.key(spaceID, sessionID)
	store.Write(spaceID, sessionID, runID, "info", "run.claimed", "original")
	store.drain(ctx, check, map[string]int64{})
	remote.data[0] = '!'
	if clean, err := store.flush(ctx, check, map[string]int64{}, key); !clean || !errors.Is(err, storage.ErrConflict) {
		t.Fatalf("conflict: clean=%t err=%v", clean, err)
	}
	store.Write(spaceID, sessionID, runID, "info", "run.finalizing", "preserve")
	store.drain(ctx, check, map[string]int64{})
	if len(remote.appends) != 1 {
		t.Fatal("conflicting upload was not paused")
	}
	local, err := os.ReadFile(store.local(key))
	if err != nil || !bytes.Contains(local, []byte("original")) || !bytes.Contains(local, []byte("preserve")) {
		t.Fatalf("queued records were lost: %s (%v)", local, err)
	}
}
