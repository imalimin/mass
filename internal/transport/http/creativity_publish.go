package httptransport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/app/session"
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
	LastCreativityDebugPlan(context.Context, string, string, string) (run.CreativityPublishPlan, error)
	SaveCreativityPublishPlan(context.Context, string, string, string, run.CreativityPublishPlan) error
}

func registerCreativityPublish(register func(string, bool, endpoint), runtime *RuntimeOptions, base string) {
	plans, ok := runtime.Repo.(publishPlans)
	if !ok {
		return
	}
	handle := func(w http.ResponseWriter, r *http.Request, actor identity.User) error {
		return handleCreativityPublish(w, r, actor, runtime, plans, false)
	}
	path := base + "/creativity/sessions/{session_id}"
	register("GET "+path+"/publish", true, handle)
	register("POST "+path+"/publish", true, handle)
	register("POST "+path+"/draft-debug", true, func(w http.ResponseWriter, r *http.Request, actor identity.User) error {
		return handleCreativityPublish(w, r, actor, runtime, plans, true)
	})
}

func handleCreativityPublish(w http.ResponseWriter, r *http.Request, actor identity.User, runtime *RuntimeOptions, plans publishPlans, debug bool) error {
	ctx := r.Context()
	space, id := r.PathValue("space_id"), r.PathValue("session_id")
	confirm := r.Method == http.MethodPost
	snapshotID, modelID := r.URL.Query().Get("workspace_snapshot_id"), r.URL.Query().Get("model_id")
	var key string
	if confirm {
		var input struct {
			SnapshotID string `json:"workspace_snapshot_id"`
			ModelID    string `json:"model_id"`
			Key        string `json:"idempotency_key"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		snapshotID, modelID, key = input.SnapshotID, input.ModelID, input.Key
	}
	if !storage.ValidID(snapshotID) || modelID != "" && !storage.ValidID(modelID) || debug && !session.ValidRequestKey(key) || !debug && key != "" {
		return identity.ErrInvalid
	}
	source, err := runtime.Repo.RuntimeSession(ctx, actor.ID, space, id)
	if err != nil {
		return err
	}
	if source.Creativity == nil {
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
	if debug && len(plan.DebugDrafts) > 0 {
		if err := plans.SaveCreativityPublishPlan(ctx, actor.ID, space, id, plan); err != nil {
			return err
		}
		return createDraftDebugSession(w, r, actor, runtime, space, key, plan)
	}
	files, err := runtime.Snapshots.ReadProjectFiles(ctx, actor.ID, space, snapshot)
	if errors.Is(err, storage.ErrBlobNotFound) {
		return storage.ErrIntegrity
	}
	if err != nil {
		return err
	}
	catalogue := runtime.Service.Catalog
	project, err := catalogue.PreparePublishAgent(ctx, actor.ID, space, *source.Creativity, files, modelID)
	if err != nil {
		return err
	}
	if debug || modelID != "" && plan.SnapshotID == "" {
		model, err := runtime.Service.Identity.Repo.Model(ctx, actor.ID, space, project.Manifest().ModelID)
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
		previous, err := plans.LastCreativityDebugPlan(ctx, actor.ID, space, id)
		if err != nil {
			return err
		}
		plan, err = catalogue.NewPublishPlan(ctx, actor.ID, space, snapshotID, *source.Creativity, project, &result, previous)
		if err != nil {
			return &publishResponseError{err, result}
		}
		plan.ModelID = modelID
	}
	if err := plans.SaveCreativityPublishPlan(ctx, actor.ID, space, id, plan); err != nil {
		return &publishResponseError{err, result}
	}
	if debug {
		result, plan.DebugDrafts, err = catalogue.SaveProjectDrafts(ctx, actor.ID, space, project, plan)
		if err != nil {
			return &publishResponseError{err, result}
		}
		if err := plans.SaveCreativityPublishPlan(ctx, actor.ID, space, id, plan); err != nil {
			return &publishResponseError{err, result}
		}
		return createDraftDebugSession(w, r, actor, runtime, space, key, plan)
	}
	result, err = catalogue.PublishProject(ctx, actor.ID, space, project, plan)
	result.ModelLocked = plan.ModelID != ""
	if err != nil {
		return &publishResponseError{err, result}
	}
	writeJSON(w, http.StatusOK, result)
	return nil
}

func createDraftDebugSession(w http.ResponseWriter, r *http.Request, actor identity.User, runtime *RuntimeOptions, space, key string, plan run.CreativityPublishPlan) error {
	result, err := runtime.Service.Create(r.Context(), actor.ID, space, session.CreateInput{
		Key: key, AgentID: plan.Agent.ID, VersionID: catalog.Latest,
		ExpectedDraftRevision: plan.DebugDrafts[plan.Agent.ID], ExpectedSkillDrafts: plan.DebugDrafts,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, result)
	return nil
}
