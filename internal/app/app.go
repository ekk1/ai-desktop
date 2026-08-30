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

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/instance"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/web"
)

type Options struct {
	DataDir      string
	PortOverride int
	Version      string
}

func NewServer(dataDir string, cfg config.Config, version string, portOverride int) (*http.Server, error) {
	server, _, err := newRuntime(dataDir, cfg, version, portOverride)
	return server, err
}

func newRuntime(dataDir string, cfg config.Config, version string, portOverride int) (*http.Server, *backend.Manager, error) {
	if portOverride < 0 || portOverride > 65535 {
		return nil, nil, fmt.Errorf("port override must be between 1 and 65535")
	}
	runtimeConfig := cfg
	if portOverride != 0 {
		runtimeConfig.ListenPort = portOverride
	}
	if err := runtimeConfig.Validate(); err != nil {
		return nil, nil, fmt.Errorf("validate runtime config: %w", err)
	}
	repository, err := backend.OpenRepository(filepath.Join(dataDir, "backends", "profiles.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("open backend profiles: %w", err)
	}
	manager := backend.NewManager(repository, filepath.Join(dataDir, "backends", "crash-logs"))
	assetRepository, err := asset.OpenRepository(filepath.Join(dataDir, "assets", "index.json"), filepath.Join(dataDir, "assets", "files"))
	if err != nil {
		return nil, nil, fmt.Errorf("open asset repository: %w", err)
	}
	knowledgeRepository, err := knowledge.OpenRepository(filepath.Join(dataDir, "knowledge", "notes.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("open knowledge repository: %w", err)
	}

	server := &http.Server{
		Addr: "127.0.0.1:" + strconv.Itoa(runtimeConfig.ListenPort),
		Handler: web.NewHandler(web.Options{
			Version:           version,
			DataDir:           dataDir,
			Config:            runtimeConfig,
			BackendRepository: repository,
			BackendManager:    manager,
			AssetRepository:   assetRepository,
			KnowledgeService:  knowledge.NewService(knowledgeRepository, assetRepository),
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server, manager, nil
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
	server, manager, err := newRuntime(dataDir, cfg, options.Version, options.PortOverride)
	if err != nil {
		return err
	}

	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.ListenAndServe()
	}()

	select {
	case err := <-serverResult:
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		managerErr := manager.Shutdown(shutdownContext)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		if err != nil {
			err = fmt.Errorf("serve %s: %w", server.Addr, err)
		}
		return errors.Join(err, managerErr)
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-serverResult
		managerErr := manager.Shutdown(shutdownContext)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr, managerErr)
	}
}
