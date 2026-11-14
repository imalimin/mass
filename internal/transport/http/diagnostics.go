package httptransport

import (
	"net/http"

	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/resource/trace"
)

type runDiagnostics struct {
	Run      run.View          `json:"run"`
	Snapshot *storage.Snapshot `json:"snapshot"`
	Trace    trace.Page        `json:"trace"`
}

func (runtime *RuntimeOptions) diagnostics(w http.ResponseWriter, r *http.Request, u identity.User) error {
	query := r.URL.Query()
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
