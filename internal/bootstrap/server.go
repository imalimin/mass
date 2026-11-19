package bootstrap

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/imalimin/mass/internal/adapter/archive"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/adapter/tos"
	"github.com/imalimin/mass/internal/adapter/workspace"
	"github.com/imalimin/mass/internal/app/catalog"
	"github.com/imalimin/mass/internal/app/evaluation"
	"github.com/imalimin/mass/internal/app/identity"
	"github.com/imalimin/mass/internal/diagnostic"
	"github.com/imalimin/mass/internal/domain/storage"
	"github.com/imalimin/mass/internal/journal"
	"github.com/imalimin/mass/internal/resource"
	"github.com/imalimin/mass/internal/resource/sessionlog"
	httptransport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/internal/transport/http/ui"
)

func Serve(ctx context.Context, c Config, version string, logger *slog.Logger) error {
	store, err := mariadb.Open(ctx, os.Getenv(c.Database.DSNEnv))
	if err != nil {
		return err
	}
	defer store.DB.Close()
	if err = store.CheckSchema(ctx); err != nil {
		return err
	}
	key, _ := base64.StdEncoding.DecodeString(os.Getenv(c.Secrets.MasterKeyEnv))
	cipher, err := secrets.New(key)
	if err != nil {
		return err
	}
	service, err := identity.New(store, cipher, c.Auth.SessionTTL, key)
	if err != nil {
		return err
	}
	blobs, err := tos.New(os.Getenv(c.TOS.EndpointEnv), os.Getenv(c.TOS.RegionEnv), os.Getenv(c.TOS.BucketEnv), os.Getenv(c.TOS.AccessKeyEnv), os.Getenv(c.TOS.SecretKeyEnv))
	if err != nil {
		return err
	}
	defer blobs.Client.Close()
	workspaces, err := workspace.New(c.Runtime.WorkspaceTempRoot, storage.Limits{MaxArchiveFileBytes: c.Storage.MaxObjectBytes, MaxTotalBytes: c.Storage.MaxWorkspaceBytes})
	if err != nil {
		return err
	}
	defer workspaces.Close()
	objects := &archive.Objects{Authorize: store.CheckAccess}
	sessions := &archive.Runtime{DB: store, Objects: objects, Log: &sessionlog.Store{
		Root: filepath.Join(workspaces.LocalPath(), ".sessions"), Prefix: c.TOS.Prefix, Blobs: blobs}}
	sessions.DiagnosticLog = diagnostic.NewStore(sessions.Log.Root, sessions.Log.Prefix, blobs, logger)
	content := &resource.ContentStore{Repo: objects, Blobs: blobs, Prefix: c.TOS.Prefix, MaxBytes: storage.MaxContentObjectBytes, Inline: sessions}
	catalogStore := &archive.Catalog{Blobs: blobs, Prefix: c.TOS.Prefix, Objects: objects, Content: content, Model: store.Model, Audit: store.Audit}
	sessions.CatalogObject = catalogStore.Lookup
	objects.Lookups = []archive.ObjectLookup{catalogStore.Lookup, sessions.Lookup}
	journalService := &journal.Journal{Repo: sessions}
	snapshots := &resource.SnapshotStore{Repo: sessions, Content: content, Journal: journalService, Workspaces: workspaces, Timeout: c.Runtime.SnapshotTimeout}
	storageOptions := &httptransport.StorageOptions{Content: content, Journal: journalService, Snapshots: snapshots, Check: httptransport.CachedReadiness(func(ctx context.Context) error {
		if err := store.CheckSchema(ctx); err != nil {
			return err
		}
		return blobs.Check(ctx)
	})}
	catalogue := &catalog.Service{Repo: catalogStore, Content: content}
	runtimeOptions, lifecycle, err := StartRuntime(ctx, c, store, service, catalogue, content, workspaces, snapshots, version, logger, nil, sessions)
	if err != nil {
		return err
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), c.HTTP.ShutdownTimeout)
		defer cancel()
		if err := lifecycle.Close(shutdown); err != nil {
			logger.Error("runtime shutdown incomplete", "code", "interrupted")
		}
	}()

	evaluations := &evaluation.Service{Repo: store, MaxPerSpace: min(c.Runtime.MaxConcurrentRuns, 2), SnapshotTimeout: c.Runtime.SnapshotTimeout, Logger: logger, Calls: evaluation.Calls{
		CheckAgent: func(ctx context.Context, actor, space, agent, version string) error {
			d, err := catalogue.Draft(ctx, actor, space, "agent", agent)
			if err != nil {
				return err
			}
			v, err := catalogue.Version(ctx, actor, space, "agent", agent, version)
			if err != nil {
				return err
			}
			if d.Archived || v.Archived || v.Debug {
				return catalog.ErrConfig
			}
			return nil
		},
		Create:      runtimeOptions.Service.Create,
		Submit:      runtimeOptions.Service.Submit,
		Run:         runtimeOptions.Repo.RuntimeRun,
		Diagnostics: sessions.Diagnostics,
	}}
	evaluations.Start(ctx)
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), c.HTTP.ShutdownTimeout)
		defer cancel()
		if err := evaluations.Close(shutdown); err != nil {
			logger.Error("evaluation shutdown incomplete", "error", err)
		}
	}()
	listener, err := net.Listen("tcp", c.HTTP.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	listeners := []net.Listener{listener}
	identityOptions := httptransport.IdentityOptions{
		Service: service, Origin: strings.TrimSuffix(c.HTTP.PublicURL, "/"), Secure: c.Auth.CookieSecure,
		Storage: storageOptions, Catalog: catalogue, Runtime: runtimeOptions, Evaluation: evaluations,
	}
	if c.lanLoopback != "" {
		loopback, err := net.Listen("tcp", c.lanLoopback)
		if err != nil {
			return err
		}
		defer loopback.Close()
		listeners = append(listeners, loopback)
		identityOptions.LoopbackOrigin = httpOrigin(c.lanLoopback)
	}
	server := &http.Server{Handler: httptransport.NewHandler(ui.Assets(), version, logger, identityOptions), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	result := make(chan error, len(listeners))
	for _, listener := range listeners {
		go func() { result <- server.Serve(listener) }()
	}
	logAttrs := []any{"address", listener.Addr().String(), "public_url", c.HTTP.PublicURL, "stage", "D08"}
	if identityOptions.LoopbackOrigin != "" {
		logAttrs = append(logAttrs, "local_url", identityOptions.LoopbackOrigin)
	}
	logger.Info("MASS listening", logAttrs...)
	select {
	case err := <-result:
		_ = server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), c.HTTP.ShutdownTimeout)
		defer cancel()
		err := server.Shutdown(shutdown)
		if err != nil {
			_ = server.Close()
		}
		for range listeners {
			if serveErr := <-result; err == nil && !errors.Is(serveErr, http.ErrServerClosed) {
				err = serveErr
			}
		}
		if err != nil {
			return err
		}
		logger.Info("MASS stopped")
		return nil
	}
}
