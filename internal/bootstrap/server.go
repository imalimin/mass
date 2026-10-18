package bootstrap

import (
	"context"
	"encoding/base64"
	"errors"
	"github.com/imalimin/mass/internal/adapter/mariadb"
	"github.com/imalimin/mass/internal/adapter/secrets"
	"github.com/imalimin/mass/internal/app/identity"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

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
	listener, err := net.Listen("tcp", c.HTTP.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: httptransport.NewHandler(ui.Assets(), version, logger, httptransport.IdentityOptions{Service: service, Origin: strings.TrimSuffix(c.HTTP.PublicURL, "/"), Secure: c.Auth.CookieSecure}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	logger.Info("MASS listening", "address", listener.Addr().String(), "stage", "D02")
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
