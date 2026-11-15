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
	"github.com/imalimin/mass/internal/app/identity"
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
	listener, err := net.Listen("tcp", c.HTTP.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: httptransport.NewHandler(ui.Assets(), version, logger, httptransport.IdentityOptions{Service: service, Origin: strings.TrimSuffix(c.HTTP.PublicURL, "/"), Secure: c.Auth.CookieSecure, Storage: storageOptions, Catalog: catalogue, Runtime: runtimeOptions}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	logger.Info("MASS listening", "address", listener.Addr().String(), "stage", "D08")
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), c.HTTP.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			<-result
			return err
		}
		err := <-result
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		logger.Info("MASS stopped")
		return nil
	}
}
