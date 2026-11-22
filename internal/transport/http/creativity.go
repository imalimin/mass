package httptransport

import (
	"context"
	"errors"
	"net/http"

	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
)

func registerCreativity(register func(string, bool, endpoint), options IdentityOptions, base string) {
	runtime := options.Runtime
	registerCreativityPublish(register, runtime, base)
	configured := func() bool {
		return storage.ValidShortID(runtime.CreativityAgentSpace) && storage.ValidShortID(runtime.CreativityAgentID)
	}
	available := func(ctx context.Context, actor, space string) (bool, error) {
		if err := options.Service.CheckAccess(ctx, actor, space); err != nil {
			return false, err
		}
		if !configured() {
			return false, nil
		}
		if runtime.CreativityAgentSpace != space {
			return runtime.Service.Catalog.PublicAgentAvailable(ctx, runtime.CreativityAgentSpace, runtime.CreativityAgentID)
		}
		repo := runtime.Service.Catalog.Repo
		draft, err := repo.CatalogDraft(ctx, actor, space, "agent", runtime.CreativityAgentID)
		if errors.Is(err, identity.ErrNotFound) {
			return false, nil
		}
		if err != nil || draft.Archived {
			return false, err
		}
		versionID := ""
		if finder, ok := repo.(interface {
			LatestFormalVersion(context.Context, string, string, string, string) (catalog.Version, bool, error)
		}); ok {
			latest, _, err := finder.LatestFormalVersion(ctx, actor, space, "agent", draft.ID)
			if err != nil {
				return false, err
			}
			versionID = latest.ID
		} else {
			versionID = draft.DefaultVersionID
		}
		if versionID == "" {
			return false, nil
		}
		version, err := repo.CatalogVersion(ctx, actor, space, "agent", draft.ID, versionID)
		if errors.Is(err, identity.ErrNotFound) {
			return false, nil
		}
		return !version.Archived && !version.Debug && err == nil, err
	}
	register("GET "+base+"/creativity", true, func(w http.ResponseWriter, r *http.Request, actor identity.User) error {
		ready, err := available(r.Context(), actor.ID, r.PathValue("space_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]bool{"available": ready})
		return nil
	})
	register("POST "+base+"/creativity/sessions", true, func(w http.ResponseWriter, r *http.Request, actor identity.User) error {
		var input struct {
			Key           string `json:"idempotency_key"`
			TargetAgentID string `json:"target_agent_id"`
		}
		if err := decodeBody(w, r, &input); err != nil {
			return err
		}
		space := r.PathValue("space_id")
		if !configured() {
			return identity.ErrNotFound
		}
		session, err := runtime.Service.Create(r.Context(), actor.ID, space, sessionapp.CreateInput{
			Key: input.Key, AgentID: runtime.CreativityAgentID, AgentSourceSpaceID: runtime.CreativityAgentSpace,
			Creativity: &run.Creativity{TargetAgentID: input.TargetAgentID},
		})
		if err != nil {
			return err
		}
		if err := runtime.Service.PrepareIteration(r.Context(), actor.ID, session); err != nil {
			return err
		}
		writeJSON(w, 201, runtime.sessionResponse(session))
		return nil
	})
}
