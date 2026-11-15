package trace

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

type Archive struct {
	Root     string
	Prefix   string
	Blobs    storage.BlobStore
	Recorder *Recorder
}

type Page struct {
	Items        []json.RawMessage `json:"items"`
	Through      int64             `json:"through_sequence,string"`
	Next         int64             `json:"next_after,string"`
	HasMore      bool              `json:"has_more"`
	UploadStatus string            `json:"upload_status"`
}

func Key(prefix, space, session string) string {
	path := space + "/sessions/" + session + "/trace.jsonl"
	if prefix == "" {
		return path
	}
	return strings.TrimSuffix(prefix, "/") + "/" + path
}

func (a *Archive) locations(s run.Snapshot) (string, string) {
	key := Key(a.Prefix, s.SpaceID, s.SessionID)
	return filepath.Join(a.Root, filepath.FromSlash(key)), key
}

type lineHeader struct {
	RunID string `json:"run_id"`
	Type  string `json:"type"`
}

func readLines(ctx context.Context, reader io.Reader, visit func([]byte, lineHeader) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 32<<10), 128<<20)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		raw := scanner.Bytes()
		var header lineHeader
		if err := json.Unmarshal(raw, &header); err != nil || !storage.ValidID(header.RunID) || (header.Type != "model_step" && header.Type != "run_finished") {
			return storage.ErrIntegrity
		}
		if err := visit(raw, header); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// Save writes a full terminal batch before uploading. The runtime lease has
// one archive worker, so a failed overwrite can never race a newer overwrite.
func (a *Archive) Save(ctx context.Context, v run.View, failure *run.FailureDetail, hasPrevious bool) error {
	path, key := a.locations(v.Snapshot)
	if err := a.append(ctx, path, key, v, failure, hasPrevious); err != nil {
		return err
	}
	a.Recorder.Forget(v.ID)
	return nil
}

// Upload replaces the remote file with the latest complete local file.
func (a *Archive) Upload(ctx context.Context, s run.Snapshot) error {
	path, key := a.locations(s)
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	_, err = a.Blobs.Overwrite(ctx, key, file, info.Size(), "application/x-ndjson")
	return err
}

func (a *Archive) append(ctx context.Context, path, key string, v run.View, failure *run.FailureDetail, hasPrevious bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	dst, err := os.CreateTemp(filepath.Dir(path), ".trace-*")
	if err != nil {
		return err
	}
	defer os.Remove(dst.Name())
	defer dst.Close()

	var src io.ReadCloser
	local, err := os.Open(path)
	if err == nil {
		src = local
	} else if errors.Is(err, os.ErrNotExist) {
		err = storage.ErrBlobNotFound
		if hasPrevious {
			src, err = a.Blobs.Open(ctx, key, "")
			if errors.Is(err, storage.ErrBlobNotFound) {
				return storage.ErrIntegrity
			}
		}
	}
	if err == nil {
		found := false
		err = readLines(ctx, src, func(raw []byte, line lineHeader) error {
			found = found || (line.RunID == v.ID && line.Type == "run_finished")
			if _, err := dst.Write(raw); err != nil {
				return err
			}
			_, err := dst.WriteString("\n")
			return err
		})
		closeErr := src.Close()
		if err = errors.Join(err, closeErr); err != nil {
			return err
		}
		if found {
			if local != nil {
				return nil
			}
			return replace(dst, path)
		}
	} else if !errors.Is(err, storage.ErrBlobNotFound) {
		return err
	}

	steps := a.Recorder.Steps(v.ID)

	encoder := json.NewEncoder(dst)
	for _, step := range steps {
		if err := encoder.Encode(step); err != nil {
			return err
		}
	}
	end := RunFinished{Type: "run_finished", RunID: v.ID, Status: v.Status, StartedAt: v.StartedAt, FinishedAt: *v.FinishedAt, Error: v.Error, Failure: failure}
	if v.StartedAt != nil {
		ms := v.FinishedAt.Sub(*v.StartedAt).Milliseconds()
		end.DurationMS = &ms
	}
	if v.WorkspaceSnapshotID != "" {
		end.SnapshotID = &v.WorkspaceSnapshotID
	}
	if err := encoder.Encode(end); err != nil {
		return err
	}
	return replace(dst, path)
}

func replace(file *os.File, path string) error {
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (a *Archive) Read(ctx context.Context, v run.View, after int64, through *int64, limit int) (Page, error) {
	page := Page{Items: []json.RawMessage{}, Next: after, UploadStatus: "recording"}
	if v.FinishedAt != nil {
		page.UploadStatus = "pending"
		if v.TraceUploaded {
			page.UploadStatus = "uploaded"
		}
		path, key := a.locations(v.Snapshot)
		var reader io.ReadCloser
		file, err := os.Open(path)
		if err == nil {
			reader = file
		} else if errors.Is(err, os.ErrNotExist) {
			err = storage.ErrBlobNotFound
			if v.TraceUploaded {
				reader, err = a.Blobs.Open(ctx, key, "")
			}
		}
		if err == nil {
			defer reader.Close()
			err = readLines(ctx, reader, func(raw []byte, line lineHeader) error {
				if line.RunID == v.ID {
					page.Through++
					if page.Through > after && (through == nil || page.Through <= *through) && len(page.Items) < limit {
						page.Items = append(page.Items, append(json.RawMessage(nil), raw...))
					}
				}
				return nil
			})
		}
		if err != nil && (v.TraceUploaded || !errors.Is(err, storage.ErrBlobNotFound)) {
			return page, err
		}
		if v.TraceUploaded && page.Through == 0 {
			return page, storage.ErrIntegrity
		}
	}
	if through != nil {
		if *through < after || *through > page.Through {
			return page, storage.ErrInvalid
		}
		page.Through = *through
	}
	if after < 0 || after > page.Through || limit < 1 || limit > 100 {
		return page, storage.ErrInvalid
	}
	page.Next = after + int64(len(page.Items))
	page.HasMore = page.Next < page.Through
	return page, nil
}
