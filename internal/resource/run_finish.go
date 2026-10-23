package resource

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
)

type CleanupStore interface {
	journal.Repository
	ContentRepository
	SnapshotRepository
	State(context.Context) (run.View, *run.CleanupPlan, error)
	SavePlan(context.Context, run.CleanupPlan) error
}
type Finisher struct {
	Scope      func(run.Snapshot) CleanupStore
	Content    *ContentStore
	Workspaces Workspaces
	Timeout    time.Duration
}

func stableID(runID, label string) string {
	return uuid.NewSHA1(uuid.MustParse(runID), []byte(label)).String()
}
func SaveForRun(s run.Snapshot, at time.Time, finish *journal.Proposed) SaveRequest {
	return SaveRequest{SpaceID: s.SpaceID, SessionID: s.SessionID, RunID: s.ID, SnapshotID: stableID(s.ID, "snapshot"), ArchiveID: stableID(s.ID, "archive"), ManifestID: stableID(s.ID, "manifest"), EventID: stableID(s.ID, "snapshot.event"), AppendKey: stableID(s.ID, "finish.batch"), OccurredAt: at, Finish: finish}
}
func (f *Finisher) Finish(ctx context.Context, s run.Snapshot, outcome run.Outcome, workspaceReady bool) error {
	repo := f.Scope(s)
	j := &journal.Journal{Repo: repo}
	current, oldPlan, err := repo.State(ctx)
	if err != nil {
		return err
	}
	if run.Terminal(current.Status) {
		return nil
	}
	plan := run.CleanupPlan{Outcome: outcome, At: time.Now().UTC().Truncate(time.Microsecond), WorkspaceReady: workspaceReady}
	if oldPlan != nil {
		plan = *oldPlan
	}
	if current.CancelRequested {
		plan.Outcome = run.Outcome{Status: "cancelled", Error: "run_cancelled"}
	}
	if err = repo.SavePlan(ctx, plan); err != nil {
		return err
	}
	// Record uncertainty after execution has actually stopped, never replay actions.
	history, _, err := History(ctx, j, s.ActorID, s.SpaceID, s.SessionID)
	if err != nil {
		return err
	}
	closed := map[string]bool{}
	for _, e := range history {
		if e.RunID != s.ID {
			continue
		}
		switch e.Type {
		case "model.response.completed":
			var d journal.ResponseData
			journal.Decode(e.Data, &d)
			closed[d.AttemptID] = true
		case "model.attempt.failed":
			var d journal.AttemptFailedData
			journal.Decode(e.Data, &d)
			closed[d.AttemptID] = true
		case "tool.execution.completed":
			var d journal.ToolData
			journal.Decode(e.Data, &d)
			closed[d.CallID] = true
		case "operation.reconciled":
			closed[e.OperationID] = true
		}
	}
	for _, e := range history {
		if e.RunID != s.ID {
			continue
		}
		var pending *journal.Proposed
		switch e.Type {
		case "model.attempt.started":
			var d journal.AttemptData
			journal.Decode(e.Data, &d)
			if !closed[d.ID] {
				v := journal.New(s.ActorID, "model.attempt.failed", s.ID, journal.AttemptFailedData{AttemptID: d.ID, MessageID: d.MessageID, Error: "interrupted"})
				v.OperationID = d.ID
				pending = &v
			}
		case "tool.execution.started":
			var d journal.ToolData
			journal.Decode(e.Data, &d)
			if !closed[d.CallID] {
				v := journal.New(s.ActorID, "operation.reconciled", s.ID, journal.ReconcileData{Outcome: "unknown"})
				v.OperationID = d.CallID
				pending = &v
			}
		}
		if pending != nil {
			pending.EventID = stableID(s.ID, "reconcile:"+pending.OperationID)
			pending.OccurredAt = plan.At
			pending.Actor.Kind = "system"
			if _, err = AppendRun(ctx, j, s, *pending); err != nil {
				return err
			}
		}
	}
	finish := journal.New(s.ActorID, "run.finished", s.ID, journal.FinishData{Status: plan.Outcome.Status, Error: plan.Outcome.Error, FinalResponseEventID: plan.Outcome.FinalResponseEventID})
	finish.EventID = stableID(s.ID, "finished")
	finish.OccurredAt = plan.At
	finish.Actor.Kind = "system"
	request := SaveForRun(s, plan.At, &finish)
	content := *f.Content
	content.Repo = repo
	snapshots := &SnapshotStore{Repo: repo, Content: &content, Journal: j, Workspaces: f.Workspaces, Timeout: f.Timeout, ActorKind: "system"}
	if plan.WorkspaceReady {
		_, err = snapshots.Save(ctx, s.ActorID, request)
		if errors.Is(err, run.ErrCancelled) {
			plan.Outcome = run.Outcome{Status: "cancelled", Error: "run_cancelled"}
			repo.SavePlan(ctx, plan)
			finish.Data, _ = json.Marshal(journal.FinishData{Status: "cancelled", Error: "run_cancelled"})
			request.Finish = &finish
			_, err = snapshots.Save(ctx, s.ActorID, request)
		}
		if errors.Is(err, run.ErrPermission) {
			plan.Outcome = run.Outcome{Status: "failed", Error: "permission_revoked"}
			repo.SavePlan(ctx, plan)
			finish.Data, _ = json.Marshal(journal.FinishData{Status: "failed", Error: "permission_revoked"})
			request.Finish = &finish
			_, err = snapshots.Save(ctx, s.ActorID, request)
		}
		if err == nil {
			return nil
		}
	}
	// A failed snapshot changes a successful outcome to failed and blocks the next run.
	current, _, stateErr := repo.State(ctx)
	if stateErr != nil {
		return errors.Join(err, stateErr)
	}
	if run.Terminal(current.Status) {
		return nil
	}
	status, code := plan.Outcome.Status, plan.Outcome.Error
	if status == "succeeded" {
		status = "failed"
		code = "workspace_snapshot_failed"
	}
	if current.CancelRequested {
		status = "cancelled"
		code = "run_cancelled"
	}
	finish.Data, _ = json.Marshal(journal.FinishData{Status: status, Error: code, SnapshotError: "workspace_snapshot_failed"})
	failure := journal.New(s.ActorID, "workspace.snapshot.failed", s.ID, journal.FailureData{Reason: "workspace_snapshot_failed"})
	failure.EventID = stableID(s.ID, "snapshot.failed")
	failure.OccurredAt = plan.At
	failure.Actor.Kind = "system"
	_, saveErr := AppendRun(ctx, j, s, failure, finish)
	return saveErr
}
func (f *Finisher) Retry(ctx context.Context, actor string, s run.Snapshot, at time.Time, snapshots *SnapshotStore) (storage.Snapshot, error) {
	request := SaveForRun(s, at, nil)
	request.AppendKey = stableID(s.ID, "snapshot.retry")
	return snapshots.Save(ctx, actor, request)
}
