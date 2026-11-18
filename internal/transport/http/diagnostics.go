package httptransport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/trace"
)

type sessionDiagnostics interface {
	Diagnostics(context.Context, string, string, string) ([]json.RawMessage, error)
}

func diagnosticIDs(r *http.Request) (sessionID, runID string, err error) {
	query := r.URL.Query()
	sessionID, runID = query.Get("session_id"), query.Get("run_id")
	if len(query) != 1 || (runID == "") == (sessionID == "") ||
		!storage.ValidShortID(runID+sessionID) || len(query["run_id"])+len(query["session_id"]) != 1 {
		return "", "", identity.ErrInvalid
	}
	return sessionID, runID, nil
}

func (runtime *RuntimeOptions) diagnosticLogs(w http.ResponseWriter, r *http.Request, u identity.User) error {
	repo, ok := runtime.Repo.(interface {
		DiagnosticScope(context.Context, string, string, string) (string, string, error)
	})
	if !ok || runtime.DiagnosticLog == nil {
		return identity.ErrNotFound
	}
	session, id, err := diagnosticIDs(r)
	if err != nil {
		return err
	}
	space, session, err := repo.DiagnosticScope(r.Context(), u.ID, session, id)
	if err != nil {
		return err
	}
	ctx, stop, err := runtime.Service.Identity.WatchAccess(r.Context(), u.ID, space)
	if err != nil {
		return err
	}
	defer stop()
	file, err := runtime.DiagnosticLog.Open(ctx, space, session)
	if err != nil && !errors.Is(err, storage.ErrBlobNotFound) {
		return err
	}
	w.Header().Set("Content-Type", diagnostic.MediaType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+session+`.jsonl"`)
	if file == nil {
		w.WriteHeader(http.StatusOK)
		return nil
	}
	defer file.Close()
	if err := diagnostic.Copy(ctx, w, file, id); err != nil {
		// An incomplete download must be visible to the client, including when
		// some rows were already sent before cancellation or a read error.
		panic(http.ErrAbortHandler)
	}
	return nil
}

type runDiagnostics struct {
	Run      run.View          `json:"run"`
	Snapshot *storage.Snapshot `json:"snapshot"`
	Trace    trace.Page        `json:"trace"`
}

func (runtime *RuntimeOptions) diagnostics(w http.ResponseWriter, r *http.Request, u identity.User) error {
	query := r.URL.Query()
	if repo, ok := runtime.Repo.(sessionDiagnostics); ok {
		sessionID, runID, err := diagnosticIDs(r)
		if err != nil {
			return err
		}
		items, err := repo.Diagnostics(r.Context(), u.ID, sessionID, runID)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return nil
	}
	runID, snapshotID := query.Get("run_id"), query.Get("snapshot_id")
	if (runID == "") == (snapshotID == "") ||
		runID != "" && !storage.ValidID(runID) || snapshotID != "" && !storage.ValidID(snapshotID) {
		return identity.ErrInvalid
	}
	after, through, limit, err := journalQuery(r)
	if err != nil {
		return err
	}
	ctx := r.Context()
	space, err := runtime.Repo.RuntimeDiagnosticSpace(ctx, u.ID, runID, snapshotID)
	if err != nil {
		return err
	}
	result := runDiagnostics{}
	if snapshotID != "" {
		snapshot, err := runtime.Snapshots.Repo.Snapshot(ctx, u.ID, space, snapshotID)
		if err != nil {
			return err
		}
		result.Snapshot = &snapshot
		runID = snapshot.SourceRunID
	}
	result.Run, err = runtime.Repo.RuntimeRun(ctx, u.ID, space, runID)
	if err != nil {
		return err
	}
	if result.Snapshot == nil && result.Run.WorkspaceSnapshotID != "" {
		snapshot, err := runtime.Snapshots.Repo.Snapshot(ctx, u.ID, space, result.Run.WorkspaceSnapshotID)
		if err != nil {
			return err
		}
		result.Snapshot = &snapshot
	}
	result.Trace, err = runtime.Trace.Read(ctx, result.Run, after, through, limit)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}
