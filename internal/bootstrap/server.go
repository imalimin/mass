package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	httptransport "github.com/imalimin/mass/internal/transport/http"
	"github.com/imalimin/mass/internal/transport/http/ui"
)

func Serve(ctx context.Context, c Config, version string, logger *slog.Logger) error {
	listener, err := net.Listen("tcp", c.HTTP.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: httptransport.NewHandler(ui.Assets(), version, logger), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	logger.Info("MASS listening", "address", listener.Addr().String(), "stage", "D00")
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
