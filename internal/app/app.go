package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/instance"
	"github.com/ekk1/ai-desktop/internal/web"
)

type Options struct {
	DataDir      string
	PortOverride int
	Version      string
}

func NewServer(dataDir string, cfg config.Config, version string, portOverride int) (*http.Server, error) {
	if portOverride < 0 || portOverride > 65535 {
		return nil, fmt.Errorf("port override must be between 1 and 65535")
	}
	runtimeConfig := cfg
	if portOverride != 0 {
		runtimeConfig.ListenPort = portOverride
	}
	if err := runtimeConfig.Validate(); err != nil {
		return nil, fmt.Errorf("validate runtime config: %w", err)
	}

	return &http.Server{
		Addr:              "127.0.0.1:" + strconv.Itoa(runtimeConfig.ListenPort),
		Handler:           web.NewHandler(web.Options{Version: version, DataDir: dataDir, Config: runtimeConfig}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}, nil
}

func Run(ctx context.Context, options Options) error {
	dataDir, err := config.ResolveDataDir(options.DataDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory %q: %w", dataDir, err)
	}
	lock, err := instance.Acquire(filepath.Join(dataDir, "instance.lock"))
	if err != nil {
		return err
	}
	defer lock.Close()

	cfg, err := config.Load(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return err
	}
	server, err := NewServer(dataDir, cfg, options.Version, options.PortOverride)
	if err != nil {
		return err
	}

	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.ListenAndServe()
	}()

	select {
	case err := <-serverResult:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve %s: %w", server.Addr, err)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-serverResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr)
	}
}
