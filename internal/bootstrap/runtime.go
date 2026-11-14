package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/imalimin/mass/internal/adapter/codevm"
	einoadapter "github.com/imalimin/mass/internal/adapter/eino"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/identity"
	sessionapp "github.com/imalimin/mass/internal/app/session"
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
func StartRuntime(parent context.Context, c Config, store *mariadb.Store, auth *identity.Service, catalogue *catalog.Service, content *resource.ContentStore, workspaces resource.Workspaces, snapshots *resource.SnapshotStore, build string, logger *slog.Logger, transport modelgateway.Transport) (options *httptransport.RuntimeOptions, lifecycle *runtimeLifecycle, err error) {
	lease, err := store.OpenRuntime(parent)
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
	journalService := &journal.Journal{Repo: store}
	traceRoot := c.Runtime.WorkspaceTempRoot
	if local, ok := workspaces.(interface{ LocalPath() string }); ok {
		traceRoot = local.LocalPath()
	}
	recorder := &trace.Recorder{}
	archive := &trace.Archive{Root: filepath.Join(traceRoot, ".traces"), Prefix: content.Prefix, Blobs: content.Blobs, Recorder: recorder}
	store.UIContent = content
	store.UIToolState = recorder.ToolState
	registryService := &registry.Registry{}
	workspaceService := &resource.RunWorkspaces{Workspaces: workspaces, Repo: store, Content: content, Journal: journalService}
	finisher := &resource.Finisher{Scope: func(s run.Snapshot) resource.CleanupStore { return lease.Cleanup(s) }, Content: content, Workspaces: workspaces, Timeout: c.Runtime.SnapshotTimeout}
	supervisorService := &supervisor.Supervisor{Guard: auth, Coordinator: lease, Workspaces: workspaceService, Finisher: finisher, CleanupTimeout: c.Runtime.SnapshotTimeout + 5*time.Second}
	schedulerService := &scheduler.Scheduler{Repo: lease, Launcher: supervisorService, MaxConcurrent: c.Runtime.MaxConcurrentRuns, MaxPerSpace: min(c.Runtime.MaxConcurrentRuns, 2)}
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
	policy := run.ToolPolicy{Version: "mass-tools-v1", MaxCodeBytes: c.Tools.Code.MaxSourceBytes, TimeoutMS: c.Tools.Code.Timeout.Milliseconds(), MaxInputBytes: int64(c.Tools.Code.MaxInputBytes), MaxOutputBytes: int64(c.Tools.Code.MaxOutputBytes), MaxFileOperations: c.Tools.Code.MaxFileOperations, MaxNetworkRequests: c.Tools.Code.MaxNetworkRequests, MaxNetworkBodyBytes: int64(c.Tools.Code.MaxNetworkBodyBytes), MaxLogBytes: c.Tools.Code.MaxLogBytes}
	if policy.MaxCodeBytes == 0 {
		policy.MaxCodeBytes = 65536
	}
	if policy.TimeoutMS == 0 {
		policy.TimeoutMS = 5000
	}
	if policy.MaxInputBytes == 0 {
		policy.MaxInputBytes = 2 << 20
	}
	if policy.MaxOutputBytes == 0 {
		policy.MaxOutputBytes = 2 << 20
	}
	if policy.MaxFileOperations == 0 {
		policy.MaxFileOperations = 100
	}
	if policy.MaxNetworkRequests == 0 {
		policy.MaxNetworkRequests = 10
	}
	if policy.MaxNetworkBodyBytes == 0 {
		policy.MaxNetworkBodyBytes = 2 << 20
	}
	if policy.MaxLogBytes == 0 {
		policy.MaxLogBytes = 65536
	}
	codeEngine := codevm.New(c.Tools.Code.MaxConcurrent, filepath.Join(traceRoot, ".go-wasi"))
	supervisorService.NewRunner = func(ctx context.Context, s run.Snapshot, started time.Time) (supervisor.Runner, error) {
		budget := control.New(s, started)
		contextPlanner := &planner.Planner{Journal: journalService, Content: content, Projector: &projector.Projector{Content: content, ToolResult: recorder.ToolResult}, Tools: registryService}
		gateway := &modelgateway.Gateway{Journal: journalService, Content: content, Transport: transport, Credentials: auth, Control: budget, Trace: recorder, Delta: store.LiveUI.Delta}
		contextPlanner.Model = gateway
		fileService := &resource.FileService{Workspaces: workspaces, Content: content, Cursors: auth, MaxBytes: min(c.Storage.MaxFileBytes, 1<<20), Limits: storage.Limits{MaxFileBytes: c.Storage.MaxFileBytes, MaxTotalBytes: c.Storage.MaxWorkspaceBytes, MaxEntries: c.Storage.MaxEntries}, Check: func(ctx context.Context, _ run.Snapshot) error { return budget.Check(ctx) }}
		fileService.EnableRunPagination()
		code := &resource.CodeService{Engine: codeEngine, Files: fileService, Network: &resource.Network{}}
		tools := &dispatcher.Dispatcher{Registry: registryService, Handler: &builtin.Handler{Files: fileService, Sources: &resource.Sources{Files: fileService, Journal: journalService}, Code: code, Journal: journalService}, Control: budget, Journal: journalService, Trace: recorder}
		return &executor.Executor{Planner: contextPlanner, Model: gateway, Tools: tools, Control: budget}, nil
	}
	// The connection-scoped lock proves the old dispatcher is gone before recovery.
	interrupted, err := lease.RecoveryCandidates(ctx)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	for _, s := range interrupted {
		if err = lease.AdoptInterrupted(ctx, s); err != nil {
			cancel()
			return nil, nil, err
		}
		if err = workspaces.With(ctx, s.SpaceID, s.SessionID, func(dir resource.WorkspaceDir) error { return dir.Clear() }); err != nil {
			cancel()
			return nil, nil, err
		}
		plan := run.CleanupPlan{Outcome: run.Outcome{Status: "failed", Error: "interrupted"}, At: time.Now().UTC().Truncate(time.Microsecond), WorkspaceReady: false}
		if err = lease.Cleanup(s).SavePlan(ctx, plan); err != nil {
			cancel()
			return nil, nil, err
		}
		cleanup, stop := context.WithTimeout(ctx, c.Runtime.SnapshotTimeout)
		err = finisher.Finish(cleanup, s, plan.Outcome, false)
		stop()
		if err != nil {
			cancel()
			return nil, nil, err
		}
	}
	if _, err = store.DB.ExecContext(ctx, "UPDATE sessions SET workspace_maintenance=FALSE,workspace_maintenance_actor=NULL"); err != nil {
		cancel()
		return nil, nil, err
	}
	service := &sessionapp.Service{ModelAttempts: max(1, c.LLM.MaxAttempts), ToolPolicy: policy, Repo: store, Catalog: catalogue, Identity: auth, Content: content, Build: build, Ready: schedulerService.Ready, Notify: schedulerService.Notify, ResolveTools: registryService.Describe}
	options = &httptransport.RuntimeOptions{Service: service, Repo: store, UI: &agui.Projector{Repo: store, Live: store.LiveUI, ResponseText: store.UIResponseText, ToolState: recorder.ToolState}, Trace: archive, Workspaces: workspaceService, Snapshots: snapshots, Finisher: finisher, Cancel: supervisorService.Cancel}
	saveTraces := func(ctx context.Context) map[string][]run.View {
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
