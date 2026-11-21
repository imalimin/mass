package httptransport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

var (
	errPublishSnapshot    = errors.New("publish_snapshot_not_current")
	errPublishModelChoice = errors.New("publish_model_unavailable")
	errPublishModelLocked = errors.New("publish_model_locked")
)

type publishResponseError struct {
	error
	result catalog.PublishManifest
}

func (e *publishResponseError) Unwrap() error { return e.error }

type publishPlans interface {
	CreativityPublishPlan(context.Context, string, string, string, string) (run.CreativityPublishPlan, error)
	SaveCreativityPublishPlan(context.Context, string, string, string, run.CreativityPublishPlan) error
}

func registerCreativityPublish(register func(string, bool, endpoint), runtime *RuntimeOptions, base string) {
	plans, ok := runtime.Repo.(publishPlans)
	if !ok {
		return
	}
	handle := func(w http.ResponseWriter, r *http.Request, actor identity.User) error {
		ctx := r.Context()
		space, id := r.PathValue("space_id"), r.PathValue("session_id")
		confirm := r.Method == http.MethodPost
		snapshotID, modelID := r.URL.Query().Get("workspace_snapshot_id"), r.URL.Query().Get("model_id")
		if confirm {
			var input struct {
				SnapshotID string `json:"workspace_snapshot_id"`
				ModelID    string `json:"model_id"`
			}
			if err := decodeBody(w, r, &input); err != nil {
				return err
			}
			snapshotID, modelID = input.SnapshotID, input.ModelID
		}
		if !storage.ValidID(snapshotID) || modelID != "" && !storage.ValidID(modelID) {
			return identity.ErrInvalid
		}
		session, err := runtime.Repo.RuntimeSession(ctx, actor.ID, space, id)
		if err != nil {
			return err
		}
		if session.Creativity == nil {
			return identity.ErrNotFound
		}
		if confirm {
			if err := runtime.Repo.BeginWorkspaceMaintenance(ctx, actor.ID, space, id); err != nil {
				return err
			}
			defer func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
				defer cancel()
				_ = runtime.Repo.EndWorkspaceMaintenance(cleanup, actor.ID, space, id)
			}()
		}
		last, err := runtime.Repo.RuntimeLastRun(ctx, actor.ID, space, id)
		if errors.Is(err, identity.ErrNotFound) {
			return errPublishSnapshot
		}
		if err != nil {
			return err
		}
		if !run.Terminal(last.Status) {
			return storage.ErrActive
		}
		if last.WorkspaceSnapshotID != snapshotID || last.FinishedAt == nil {
			return errPublishSnapshot
		}
		snapshot, err := runtime.Snapshots.Repo.Snapshot(ctx, actor.ID, space, snapshotID)
		if err != nil {
			return err
		}
		if snapshot.SessionID != id || snapshot.SourceRunID != last.ID {
			return errPublishSnapshot
		}
		plan, err := plans.CreativityPublishPlan(ctx, actor.ID, space, id, snapshotID)
		if err != nil {
			return err
		}
		if plan.ModelID != "" {
			if modelID != "" && modelID != plan.ModelID {
				return errPublishModelLocked
			}
			modelID = plan.ModelID
		}
		files, err := runtime.Snapshots.ReadProjectFiles(ctx, actor.ID, space, snapshot)
		if errors.Is(err, storage.ErrBlobNotFound) {
			return storage.ErrIntegrity
		}
		if err != nil {
			return err
		}
		catalogue := runtime.Service.Catalog
		project, err := catalogue.PreparePublishAgent(ctx, actor.ID, space, *session.Creativity, files, modelID)
		if err != nil {
			return err
		}
		if modelID != "" && plan.SnapshotID == "" {
			model, err := runtime.Service.Identity.Repo.Model(ctx, actor.ID, space, modelID)
			if errors.Is(err, identity.ErrNotFound) || err == nil && !project.SupportsModel(model) {
				return errPublishModelChoice
			}
			if err != nil {
				return err
			}
		}
		project, err = catalogue.ScanPublishSkills(ctx, actor.ID, space, files, project)
		if err != nil {
			return err
		}
		if !confirm {
			result := project.Manifest()
			result.ModelLocked = plan.ModelID != ""
			writeJSON(w, http.StatusOK, result)
			return nil
		}
		result := project.PendingResult()
		if plan.SnapshotID == "" {
			plan, err = catalogue.NewPublishPlan(ctx, actor.ID, space, snapshotID, *session.Creativity, project, &result)
			if err != nil {
				return &publishResponseError{err, result}
			}
			plan.ModelID = modelID
		}
		if err := plans.SaveCreativityPublishPlan(ctx, actor.ID, space, id, plan); err != nil {
			return &publishResponseError{err, result}
		}
		result, err = catalogue.PublishProject(ctx, actor.ID, space, project, plan)
		result.ModelLocked = plan.ModelID != ""
		if err != nil {
			return &publishResponseError{err, result}
		}
		writeJSON(w, http.StatusOK, result)
		return nil
	}
	path := base + "/creativity/sessions/{session_id}/publish"
	register("GET "+path, true, handle)
	register("POST "+path, true, handle)
}
