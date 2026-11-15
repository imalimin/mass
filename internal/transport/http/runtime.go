package httptransport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/trace"
	"github.com/imalimin/mass/internal/transport/agui"
)

type RuntimeRepository interface {
	RuntimePlan(context.Context, string, string, string) (run.Plan, error)
	BeginWorkspaceMaintenance(context.Context, string, string, string) error
	EndWorkspaceMaintenance(context.Context, string, string, string) error
	RuntimeLastRun(context.Context, string, string, string) (run.View, error)
	RuntimeMessagesBefore(context.Context, string, string, string, int64, int) ([]run.MessageView, error)
	sessionapp.Repository
	RuntimeRuns(context.Context, string, string, string, identity.Query) ([]run.View, error)
	RuntimeTrace(context.Context, string, string, string, int64, *int64, int) (journal.Page, error)
	RuntimeDiagnosticSpace(context.Context, string, string, string) (string, error)
	RuntimeSnapshots(context.Context, string, string, string) ([]storage.Snapshot, error)
	RuntimeCleanupPlan(context.Context, string, string, string) (run.CleanupPlan, error)
}
type RuntimeOptions struct {
	Service    *sessionapp.Service
	Repo       RuntimeRepository
	UI         *agui.Projector
	Trace      *trace.Archive
	Workspaces *resource.RunWorkspaces
	Snapshots  *resource.SnapshotStore
	Finisher   *resource.Finisher
	Cancel     func(string)
}

func registerRuntime(mux *http.ServeMux, register func(string, bool, endpoint), options IdentityOptions, query func(*http.Request, identity.User) (identity.Query, int, string, error), token func(*http.Request) string, logger *slog.Logger) {
	runtime := options.Runtime
	if runtime == nil {
		return
	}
	svc := runtime.Service
	base := "/api/v1/spaces/{space_id}"
	register("POST "+base+"/sessions/{session_id}/attachments", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b struct {
			Name      string `json:"name"`
			MediaType string `json:"media_type"`
			Content   string `json:"content_base64"`
		}
		if err := decodeCatalog(w, r, &b, 12<<20); err != nil {
			return err
		}
		raw, err := base64.StdEncoding.Strict().DecodeString(b.Content)
		if err != nil {
			return identity.ErrInvalid
		}
		a, err := svc.UploadAttachment(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("session_id"), b.Name, b.MediaType, bytes.NewReader(raw))
		if err != nil {
			return err
		}
		writeJSON(w, 201, a)
		return nil
	})
	register("GET "+base+"/runs/{run_id}/plan", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		v, err := runtime.Repo.RuntimePlan(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, v)
		return nil
	})
	register("POST "+base+"/sessions", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b sessionapp.CreateInput
		if err := decodeBody(w, r, &b); err != nil {
			return err
		}
		v, err := svc.Create(r.Context(), u.ID, r.PathValue("space_id"), b)
		if err != nil {
			return err
		}
		writeJSON(w, 201, v)
		return nil
	})
	listSessions := func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		agent := r.PathValue("agent_id")
		if agent != "" && !storage.ValidID(agent) {
			return storage.ErrInvalid
		}
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := svc.Repo.RuntimeSessions(r.Context(), u.ID, r.PathValue("space_id"), agent, q)
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v run.Session) string { return v.ID }))
		return nil
	}
	register("GET "+base+"/sessions", true, listSessions)
	register("GET "+base+"/agents/{agent_id}/sessions", true, listSessions)
	register("GET "+base+"/sessions/{session_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		v, err := svc.Repo.RuntimeSession(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("session_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, v)
		return nil
	})
	register("POST "+base+"/sessions/{session_id}/runs", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		var b sessionapp.SubmitInput
		if err := decodeCatalog(w, r, &b, 1<<20); err != nil {
			return err
		}
		v, err := svc.Submit(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("session_id"), b)
		if err != nil {
			return err
		}
		writeJSON(w, 202, v)
		return nil
	})
	register("GET "+base+"/sessions/{session_id}/runs", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		q, n, scope, err := query(r, u)
		if err != nil {
			return err
		}
		items, err := runtime.Repo.RuntimeRuns(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("session_id"), q)
		if err != nil {
			return err
		}
		writeJSON(w, 200, identity.Paginate(options.Service, scope, items, n, func(v run.View) string { return v.ID }))
		return nil
	})
	register("GET "+base+"/runs/{run_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		v, err := svc.Repo.RuntimeRun(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, v)
		return nil
	})
	register("GET /api/v1/diagnostics", true, runtime.diagnostics)
	register("POST "+base+"/runs/{run_id}/cancel", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		v, err := svc.Repo.CancelRuntimeRun(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"))
		if err != nil {
			return err
		}
		if runtime.Cancel != nil {
			runtime.Cancel(v.ID)
		}
		writeJSON(w, 200, v)
		return nil
	})
	register("GET "+base+"/sessions/{session_id}/messages", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		after, _, limit, err := journalQuery(r)
		if err != nil {
			return err
		}
		space, session := r.PathValue("space_id"), r.PathValue("session_id")
		beforeSequence := r.URL.Query().Get("before_sequence")
		var items []run.MessageView
		if beforeSequence != "" {
			before, parseErr := agui.ParseCursor(beforeSequence)
			if parseErr != nil || r.URL.Query().Has("after_sequence") {
				return storage.ErrInvalid
			}
			items, err = runtime.Repo.RuntimeMessagesBefore(r.Context(), u.ID, space, session, before, limit)
		} else {
			items, err = svc.Repo.RuntimeMessages(r.Context(), u.ID, space, session, after, limit)
		}
		if err != nil {
			return err
		}
		more := len(items) == limit
		if beforeSequence != "" {
			more = len(items) > limit
			if more {
				items = items[:limit]
			}
			slices.Reverse(items)
		}
		type item struct {
			run.MessageView
			Content   string `json:"content"`
			Truncated bool   `json:"truncated"`
		}
		result := []item{}
		next := after
		for _, m := range items {
			raw, err := resource.ReadBytes(r.Context(), svc.Content, u.ID, r.PathValue("space_id"), m.ContentID, 4<<20)
			if err != nil {
				return err
			}
			text := string(raw)
			if m.Role == "assistant" {
				var reply run.Reply
				if json.Unmarshal(raw, &reply) != nil {
					return storage.ErrIntegrity
				}
				text = reply.Message.Content
			}
			truncated := len(text) > 8192
			if truncated {
				text = text[:8192]
				for !utf8.ValidString(text) {
					text = text[:len(text)-1]
				}
			}
			result = append(result, item{MessageView: m, Content: text, Truncated: truncated})
			next = m.Sequence
		}
		writeJSON(w, 200, map[string]any{"items": result, "next_after": strconv.FormatInt(next, 10), "has_more": more})
		return nil
	})
	for _, kind := range []string{"trace", "contexts"} {
		register("GET "+base+"/runs/{run_id}/"+kind, true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
			after, through, limit, err := journalQuery(r)
			if err != nil {
				return err
			}
			page, err := runtime.Repo.RuntimeTrace(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"), after, through, limit)
			if err != nil {
				return err
			}
			if kind == "contexts" {
				filtered := []journal.Event{}
				for _, e := range page.Events {
					if e.Type == "context.projected" || e.Type == "context.summary.created" {
						filtered = append(filtered, e)
					}
				}
				page.Events = filtered
			}
			writeJSON(w, 200, page)
			return nil
		})
	}
	register("GET "+base+"/runs/{run_id}/contexts/{projection_id}", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		space := r.PathValue("space_id")
		view, err := runtime.Repo.RuntimeRun(r.Context(), u.ID, space, r.PathValue("run_id"))
		if err != nil {
			return err
		}
		history, err := resource.RunHistory(r.Context(), runtime.Snapshots.Journal, u.ID, space, view.SessionID, view.ID, "context.projected")
		if err != nil {
			return err
		}
		var projection run.Prepared
		found := false
		for _, e := range history {
			if e.RunID == view.ID && e.Type == "context.projected" {
				var d journal.ProjectionData
				journal.Decode(e.Data, &d)
				if d.ID == r.PathValue("projection_id") {
					if err = resource.ReadJSON(r.Context(), svc.Content, u.ID, space, d.ContentID, 32<<20, &projection); err != nil {
						return err
					}
					found = true
					break
				}
			}
		}
		if !found {
			return identity.ErrNotFound
		}
		selected := map[string]bool{}
		for _, id := range projection.Spec.SourceEventIDs {
			selected[id] = true
		}
		sources := []journal.Event{}
		_, err = resource.WalkHistory(r.Context(), runtime.Snapshots.Journal, u.ID, space, view.SessionID, func(e journal.Event) error {
			if selected[e.EventID] {
				sources = append(sources, e)
			}
			return nil
		})
		if err != nil {
			return err
		}
		var summary *run.ContextSummary
		if projection.Spec.SummaryRef != nil {
			summary = &run.ContextSummary{}
			if err = resource.ReadJSON(r.Context(), svc.Content, u.ID, space, projection.Spec.SummaryRef.ObjectID, 32<<20, summary); err != nil {
				return err
			}
		}
		writeJSON(w, 200, map[string]any{"projection": projection, "source_events": sources, "requests": []journal.Event{}, "summary": summary})
		return nil
	})

	register("GET "+base+"/runs/{run_id}/metrics", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		v, err := runtime.Repo.RuntimeRun(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"))
		if err != nil {
			return err
		}
		groups, through, err := modelMetrics(r.Context(), runtime.Repo, u.ID, v.SpaceID, v.ID)
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"model_id": v.Definition.Model.ModelID, "model_revision": v.Definition.Model.Revision, "through_sequence": strconv.FormatInt(through, 10), "groups": groups})
		return nil
	})

	register("GET "+base+"/runs/{run_id}/artifacts", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		items, err := svc.Repo.RuntimeArtifacts(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return nil
	})
	register("GET "+base+"/artifacts/{id}/download", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		space := r.PathValue("space_id")
		artifact, err := svc.Repo.RuntimeArtifact(r.Context(), u.ID, space, r.PathValue("id"))
		if err != nil {
			return err
		}
		_, file, err := svc.Content.Read(r.Context(), u.ID, space, artifact.ObjectID)
		if err != nil {
			return err
		}
		defer file.Close()
		check := func(ctx context.Context) error {
			if _, err := options.Service.Me(ctx, token(r)); err != nil {
				return err
			}
			return options.Service.CheckAccess(ctx, u.ID, space)
		}
		if err = check(r.Context()); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Name}))
		info, err := file.Stat()
		if err != nil {
			return err
		}
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
		_ = copyAuthorized(r.Context(), w, file, check)
		return nil
	})
	register("GET "+base+"/sessions/{session_id}/workspace-snapshots", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		items, err := runtime.Repo.RuntimeSnapshots(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("session_id"))
		if err != nil {
			return err
		}
		writeJSON(w, 200, map[string]any{"items": items})
		return nil
	})
	register("POST "+base+"/sessions/{session_id}/workspace-snapshots/retry", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		space, session := r.PathValue("space_id"), r.PathValue("session_id")
		if err := runtime.Repo.BeginWorkspaceMaintenance(r.Context(), u.ID, space, session); err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = runtime.Repo.EndWorkspaceMaintenance(ctx, u.ID, space, session)
		}()

		v, err := svc.Repo.RuntimeSession(r.Context(), u.ID, space, session)
		if err != nil {
			return err
		}
		if v.WorkspaceError == "" {
			return storage.ErrConflict
		}
		if !runtime.Workspaces.Available(space, session) {
			return storage.ErrWorkspace
		}
		last, err := runtime.Repo.RuntimeLastRun(r.Context(), u.ID, space, session)
		if err != nil {
			return err
		}
		if !run.Terminal(last.Status) {
			return storage.ErrActive
		}
		plan, err := runtime.Repo.RuntimeCleanupPlan(r.Context(), u.ID, space, last.ID)
		if err != nil {
			return err
		}
		snapshot, err := runtime.Finisher.Retry(r.Context(), u.ID, last.Snapshot, plan.At, runtime.Snapshots)
		if err != nil {
			return err
		}
		writeJSON(w, 200, snapshot)
		return nil
	})
	register("POST "+base+"/sessions/{session_id}/workspace/restore", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		space, session := r.PathValue("space_id"), r.PathValue("session_id")
		if err := runtime.Repo.BeginWorkspaceMaintenance(r.Context(), u.ID, space, session); err != nil {
			return err
		}
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = runtime.Repo.EndWorkspaceMaintenance(ctx, u.ID, space, session)
		}()

		if _, err := svc.Repo.RuntimeSession(r.Context(), u.ID, space, session); err != nil {
			return err
		}
		if err := runtime.Workspaces.RestoreIdle(r.Context(), u.ID, space, session, runtime.Snapshots); err != nil {
			return err
		}
		writeJSON(w, 200, map[string]bool{"restored": true})
		return nil
	})
	register("GET "+base+"/runs/{run_id}/ui-snapshot", true, func(w http.ResponseWriter, r *http.Request, u identity.User) error {
		cp, err := runtime.UI.CatchUp(r.Context(), u.ID, r.PathValue("space_id"), r.PathValue("run_id"))
		if err != nil {
			return err
		}
		cp.SummaryAttempts = nil // Internal replay metadata is not part of the public checkpoint.
		writeJSON(w, 200, cp)
		return nil
	})
	mux.HandleFunc("GET "+base+"/runs/{run_id}/events", func(w http.ResponseWriter, r *http.Request) {
		user, err := options.Service.Me(r.Context(), token(r))
		if err != nil {
			apiError(w, err, logger)
			return
		}
		space, id := r.PathValue("space_id"), r.PathValue("run_id")
		ctx, unwatch, err := options.Service.WatchAccess(r.Context(), user.ID, space)
		if err != nil {
			apiError(w, err, logger)
			return
		}
		defer unwatch()
		cp, err := runtime.UI.CatchUp(ctx, user.ID, space, id)
		if err != nil {
			apiError(w, err, logger)
			return
		}
		after := r.URL.Query().Get("after")
		last := r.Header.Get("Last-Event-ID")
		if after != "" && last != "" && after != last {
			apiError(w, run.ErrCursor, logger)
			return
		}
		if after == "" {
			after = last
		}
		if after == "" {
			after = "0"
		}
		cursor, err := agui.ParseCursor(after)
		if streamID := r.URL.Query().Get("stream_id"); err != nil || cursor > cp.Cursor || (streamID != "" && streamID != cp.StreamID) {
			apiError(w, run.ErrCursor, logger)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		controller := http.NewResponseController(w)
		check := func() error {
			if _, err := options.Service.Me(ctx, token(r)); err != nil {
				return err
			}
			return options.Service.CheckAccess(ctx, user.ID, space)
		}
		ticker := time.NewTicker(300 * time.Millisecond)
		defer ticker.Stop()
		heartbeat := time.Now()
		for {
			if err := check(); err != nil {
				return
			}
			frames, closed, err := runtime.UI.Repo.ReadFrames(ctx, user.ID, space, id, cursor, 100)
			if err != nil {
				return
			}
			for _, frame := range frames {
				if ctx.Err() != nil {
					return
				}
				var compact bytes.Buffer
				if json.Compact(&compact, frame.JSON) != nil {
					return
				}
				_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err = fmt.Fprintf(w, "id: %d\ndata: %s\n\n", frame.Sequence, compact.Bytes()); err != nil {
					return
				}
				cursor = frame.Sequence
			}
			if len(frames) > 0 && controller.Flush() != nil {
				return
			}
			if closed {
				return
			}
			if time.Since(heartbeat) > 3*time.Second {
				_ = controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err = fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				if controller.Flush() != nil {
					return
				}
				heartbeat = time.Now()
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
