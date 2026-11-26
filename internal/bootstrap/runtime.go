package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	storearchive "github.com/imalimin/mass/internal/adapter/archive"
	"github.com/imalimin/mass/internal/adapter/command"
	einoadapter "github.com/imalimin/mass/internal/adapter/eino"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/run"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/trace"
	"github.com/imalimin/mass/internal/runtime/context/planner"
	"github.com/imalimin/mass/internal/runtime/context/projector"
	"github.com/imalimin/mass/internal/runtime/control"
	"github.com/imalimin/mass/internal/runtime/executor"
	"github.com/imalimin/mass/internal/runtime/modelgateway"
	"github.com/imalimin/mass/internal/runtime/scheduler"
	"github.com/imalimin/mass/internal/runtime/supervisor"
	"github.com/imalimin/mass/internal/runtime/tool/builtin"
	"github.com/imalimin/mass/internal/runtime/tool/dispatcher"
	"github.com/imalimin/mass/internal/runtime/tool/registry"
	"github.com/imalimin/mass/internal/transport/agui"
	httptransport "github.com/imalimin/mass/internal/transport/http"
)

type runtimeLifecycle struct {
	lease      *mariadb.RuntimeLease
	supervisor *supervisor.Supervisor
	cancel     context.CancelFunc
	saveTraces func(context.Context) map[string][]run.View
	wg         sync.WaitGroup
}

func (r *runtimeLifecycle) NodeID() string {
	if r == nil || r.lease == nil {
		return ""
	}
	return r.lease.NodeID()
}

func (r *runtimeLifecycle) PeerLocator() httptransport.PeerLocator {
	if r == nil || r.lease == nil {
		return nil
	}
	return r.lease
}

func (r *runtimeLifecycle) Close(ctx context.Context) error {
	r.cancel()
	r.supervisor.Stop()
	if err := r.supervisor.Wait(ctx); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		// The supervisor can commit terminal runs after the worker stops.
		// Save their in-memory steps before releasing the runtime lease.
		r.saveTraces(ctx)
		return r.lease.Close()
	case <-ctx.Done():
		return ctx.Err()
	}
}

type runtimeStore interface {
	httptransport.RuntimeRepository
	RunBaseSnapshot(context.Context, run.Snapshot) (storage.Snapshot, error)
	journal.Repository
	agui.Repository
}

type runCoordinator interface {
	scheduler.Repository
	supervisor.Coordinator
	RecoveryCandidates(context.Context) ([]run.Snapshot, error)
}

func StartRuntime(parent context.Context, c Config, store *mariadb.Store, auth *identity.Service, catalogue *catalog.Service, content *resource.ContentStore, workspaces resource.Workspaces, snapshots *resource.SnapshotStore, build string, logger *slog.Logger, transport modelgateway.Transport, persisted ...*storearchive.Runtime) (options *httptransport.RuntimeOptions, lifecycle *runtimeLifecycle, err error) {
	var lease *mariadb.RuntimeLease
	if len(persisted) > 0 {
		lease, err = store.OpenRuntimeNode(parent, peerBaseURL(c))
	} else {
		lease, err = store.OpenRuntime(parent)
	}
	if err != nil {
		return nil, nil, err
	}
	success := false
	defer func() {
		if !success {
			lease.Close()
		}
	}()
	ctx, cancel := context.WithCancel(parent)
	var repo runtimeStore = store
	var coordinator runCoordinator = lease
	var sessions *storearchive.Runtime
	var diagnosticLog *diagnostic.Store
	live := store.LiveUI
	responseText := store.UIResponseText
	scope := func(s run.Snapshot) resource.CleanupStore { return lease.Cleanup(s) }
	if len(persisted) > 0 {
		sessions = persisted[0]
		diagnosticLog = sessions.DiagnosticLog
		sessions.Lease, sessions.Live = lease, live
		repo, coordinator, responseText, scope = sessions, sessions, sessions.ResponseText, sessions.Cleanup
	}
	journalService := &journal.Journal{Repo: repo}
	traceRoot := c.Runtime.WorkspaceTempRoot
	if local, ok := workspaces.(interface{ LocalPath() string }); ok {
		traceRoot = local.LocalPath()
	}
	recorder := &trace.Recorder{}
	if sessions != nil {
		sessions.Recorder = recorder
	}
	toolState := recorder.ToolState
	if sessions != nil {
		toolState = sessions.ToolState
	}
	var archive *trace.Archive
	if sessions == nil {
		archive = &trace.Archive{Root: filepath.Join(traceRoot, ".traces"), Prefix: content.Prefix, Blobs: content.Blobs, Recorder: recorder}
	}
	store.UIContent = content
	store.UIToolState = recorder.ToolState
	registryService := &registry.Registry{}
	workspaceService := &resource.RunWorkspaces{Workspaces: workspaces, Repo: repo, Content: content, Journal: journalService,
		Limits: storage.Limits{MaxArchiveFileBytes: c.Storage.MaxObjectBytes, MaxTotalBytes: c.Storage.MaxWorkspaceBytes}}
	if sessions != nil {
		workspaceService.Iteration = func(ctx context.Context, actor, space, id string, write func(string, []byte) error) (bool, error) {
			session, err := sessions.RuntimeSession(ctx, actor, space, id)
			if err != nil || session.Creativity == nil || session.Creativity.TargetAgentID == "" || session.LatestSnapshotID != "" {
				return false, err
			}
			return true, catalogue.RestoreIteration(ctx, actor, space, *session.Creativity, write)
		}
	}
	finisher := &resource.Finisher{Scope: scope, Content: content, Workspaces: workspaces, Timeout: c.Runtime.SnapshotTimeout, DiagnosticLog: diagnosticLog}
	snapshots.DiagnosticLog = diagnosticLog
	supervisorService := &supervisor.Supervisor{Guard: auth, Coordinator: coordinator, Workspaces: workspaceService, Finisher: finisher, CleanupTimeout: c.Runtime.SnapshotTimeout + 5*time.Second, DiagnosticLog: diagnosticLog}
	schedulerService := &scheduler.Scheduler{Repo: coordinator, Launcher: supervisorService}
	fault := func(err error) {
		schedulerService.Fault(err)
		cancel()
		supervisorService.Stop()
		logger.Error("runtime stopped; restart required", "code", "runtime_unavailable")
	}
	supervisorService.Fault = fault
	if transport == nil {
		transport = &einoadapter.ModelTransport{Timeout: c.LLM.Timeout}
	}
	policy := run.ToolPolicy{Version: "mass-tools-v1", TimeoutMS: c.Tools.Code.Timeout.Milliseconds(), MaxLogBytes: c.Tools.Code.MaxLogBytes}
	if policy.TimeoutMS == 0 {
		policy.TimeoutMS = 5000
	}
	if policy.MaxLogBytes == 0 {
		policy.MaxLogBytes = 65536
	}
	skillCache := &resource.SkillCache{
		Root:               filepath.Join(traceRoot, ".skill-cache"),
		SharedSkillPackage: catalogue.OpenPublicAgentSkillPackage,
	}
	supervisorService.NewRunner = func(ctx context.Context, s run.Snapshot, started time.Time, missingLocalFiles []string) (supervisor.Runner, error) {
		budget := control.New(s, started)
		budget.DiagnosticContext = ctx
		runJournal, runContent := journalService, content
		if sessions != nil {
			runJournal, runContent = sessions.RunServices(s, content)
		}
		contextPlanner := &planner.Planner{Journal: runJournal, Content: runContent, Projector: &projector.Projector{
			Content: runContent, ToolResult: recorder.ToolResult, MissingLocalFiles: missingLocalFiles,
		}, Tools: registryService}
		var credentials modelgateway.Credentials = auth
		if s.AgentSourceSpaceID != "" {
			credentials = identity.SharedRunCredentials{Identity: auth, Run: s}
		}
		gateway := &modelgateway.Gateway{Journal: runJournal, Content: runContent, Transport: transport, Credentials: credentials, Control: budget, Trace: recorder, Delta: store.LiveUI.Delta}
		contextPlanner.Model = gateway
		fileService := &resource.FileService{Workspaces: workspaces, Content: runContent, Skills: skillCache, MaxBytes: 1 << 20, Limits: storage.Limits{MaxArchiveFileBytes: c.Storage.MaxObjectBytes, MaxTotalBytes: c.Storage.MaxWorkspaceBytes}, Check: func(ctx context.Context, _ run.Snapshot) error { return budget.Check(ctx) }}
		commands := &resource.CommandService{
			Workspaces:         workspaces,
			Content:            runContent,
			Skills:             skillCache,
			ExecuteWithOptions: command.ExecuteWithOptions,
			Env: func(s run.Snapshot) ([]string, func(), error) {
				apiURL := localAPIBaseURL(c)
				if apiURL == "" || auth == nil {
					return nil, nil, nil
				}
				tok, revoke, err := auth.IssueRunReadToken(s.SpaceID, s.ActorID)
				if err != nil {
					return nil, nil, err
				}
				return []string{
					"MASS_API_URL=" + apiURL,
					"MASS_SPACE_ID=" + s.SpaceID,
					"MASS_RUN_TOKEN=" + tok,
				}, revoke, nil
			},
		}
		tools := &dispatcher.Dispatcher{Registry: registryService, Handler: &builtin.Handler{Files: fileService, Command: commands, Journal: runJournal}, Control: budget, Journal: runJournal, Trace: recorder}
		return &executor.Executor{Planner: contextPlanner, Model: gateway, Tools: tools, Control: budget}, nil
	}
	recoverExpired := func(rctx context.Context) error {
		interrupted, err := coordinator.RecoveryCandidates(rctx)
		if err != nil {
			return err
		}
		for _, s := range interrupted {
			if sessions != nil {
				done, err := sessions.Recover(rctx, s)
				if err != nil {
					return err
				}
				if done {
					continue
				}
			} else if err = lease.AdoptInterrupted(rctx, s); err != nil {
				return err
			}
			plan := run.CleanupPlan{Outcome: run.Outcome{Status: "failed", Error: "interrupted"}, At: time.Now().UTC().Truncate(time.Microsecond), WorkspaceReady: false}
			diagnostic.Record(diagnostic.WithRun(rctx, diagnosticLog, s), "warn", "run.interrupted", "reason=recovery state=running")
			if err = scope(s).SavePlan(rctx, plan); err != nil {
				return err
			}
			cleanup, stop := context.WithTimeout(rctx, c.Runtime.SnapshotTimeout)
			err = finisher.Finish(cleanup, s, plan)
			stop()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if err := recoverExpired(ctx); err != nil {
		cancel()
		return nil, nil, err
	}
	if sessions == nil {
		if _, err = store.DB.ExecContext(ctx, "UPDATE sessions SET workspace_maintenance=FALSE,workspace_maintenance_actor=NULL"); err != nil {
			cancel()
			return nil, nil, err
		}
	}
	service := &sessionapp.Service{ModelAttempts: max(1, c.LLM.MaxAttempts), ToolPolicy: policy, Repo: repo, Catalog: catalogue, Identity: auth, Content: content, Workspaces: workspaceService, Build: build, Ready: schedulerService.Ready, Notify: schedulerService.Notify, ResolveTools: registryService.Describe}
	options = &httptransport.RuntimeOptions{Service: service, Repo: repo, UI: &agui.Projector{Repo: repo, Live: live, ResponseText: responseText, ToolState: toolState}, Trace: archive, Workspaces: workspaceService, Snapshots: snapshots, Finisher: finisher, Cancel: supervisorService.Cancel, DiagnosticLog: diagnosticLog}
	options.CreativityAgentID = c.Creativity.AgentID
	options.CreativityAgentSpace = c.Creativity.AgentWorkspaceID
	saveTraces := func(ctx context.Context) map[string][]run.View {
		if sessions != nil {
			if err := sessions.RetryUploads(ctx); err != nil && ctx.Err() == nil {
				logger.Warn("session upload pending", "error", err)
			}
			return nil
		}
		jobs, err := lease.PendingTraces(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Warn("could not load pending session traces", "error", err)
		}
		// Save all pending runs locally before any network upload. A later
		// failure in one session must not erase an earlier complete batch.
		ready := make(map[string][]run.View)
		for _, job := range jobs {
			store.LiveUI.Finished(job.Run.SpaceID, job.Run.ID)
			jobCtx, stop := context.WithTimeout(ctx, 45*time.Second)
			err := archive.Save(jobCtx, job.Run, job.Failure, job.HasPrevious)
			stop()
			if err != nil {
				if ctx.Err() == nil {
					logger.Warn("could not save session trace", "run_id", job.Run.ID, "error", err)
				}
				continue
			}
			key := job.Run.SpaceID + "/" + job.Run.SessionID
			ready[key] = append(ready[key], job.Run)
		}
		return ready
	}
	lifecycle = &runtimeLifecycle{lease: lease, supervisor: supervisorService, cancel: cancel, saveTraces: saveTraces}
	if diagnosticLog != nil {
		lifecycle.wg.Add(1)
		go func() {
			defer lifecycle.wg.Done()
			diagnosticLog.Run(ctx, lease.Check)
		}()
	}
	lifecycle.wg.Add(2)
	go func() {
		defer lifecycle.wg.Done()
		if err := schedulerService.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			fault(err)
		}
	}()
	go func() {
		defer lifecycle.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		lastRecovery := time.Now()
		for {
			ready := saveTraces(ctx)
			for _, runs := range ready {
				jobCtx, stop := context.WithTimeout(ctx, 45*time.Second)
				err := archive.Upload(jobCtx, runs[0].Snapshot)
				if err == nil {
					err = lease.MarkTraceUploaded(jobCtx, runs)
				}
				stop()
				if err != nil && ctx.Err() == nil {
					logger.Warn("session trace upload pending", "session_id", runs[0].SessionID, "error", err)
				}
			}
			if sessions != nil && time.Since(lastRecovery) >= 10*time.Second {
				lastRecovery = time.Now()
				recCtx, stop := context.WithTimeout(ctx, 30*time.Second)
				if err := recoverExpired(recCtx); err != nil && ctx.Err() == nil {
					logger.Warn("expired run recovery pending", "error", err)
				}
				stop()
			}
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				store.LiveUI.Expire(now)
			}
		}
	}()
	success = true
	return options, lifecycle, nil
}
