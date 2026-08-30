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
	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/instance"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/llm"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
	"github.com/ekk1/ai-desktop/internal/web"
)

type Options struct {
	DataDir      string
	PortOverride int
	Version      string
}

func NewServer(dataDir string, cfg config.Config, version string, portOverride int) (*http.Server, error) {
	application, err := newRuntime(dataDir, cfg, version, portOverride)
	if err != nil {
		return nil, err
	}
	return application.server, nil
}

type applicationRuntime struct {
	server         *http.Server
	backendManager *backend.Manager
	llmManager     *llm.Manager
}

func newRuntime(dataDir string, cfg config.Config, version string, portOverride int) (*applicationRuntime, error) {
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
	configPath := filepath.Join(dataDir, "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		return nil, fmt.Errorf("initialize runtime config repository: %w", err)
	}
	configRepository, err := config.OpenRepository(configPath)
	if err != nil {
		return nil, fmt.Errorf("open runtime config repository: %w", err)
	}
	repository, err := backend.OpenRepository(filepath.Join(dataDir, "backends", "profiles.json"))
	if err != nil {
		return nil, fmt.Errorf("open backend profiles: %w", err)
	}
	manager := backend.NewManager(repository, filepath.Join(dataDir, "backends", "crash-logs"))
	assetRepository, err := asset.OpenRepository(filepath.Join(dataDir, "assets", "index.json"), filepath.Join(dataDir, "assets", "files"))
	if err != nil {
		return nil, fmt.Errorf("open asset repository: %w", err)
	}
	knowledgeRepository, err := knowledge.OpenRepository(filepath.Join(dataDir, "knowledge", "notes.json"))
	if err != nil {
		return nil, fmt.Errorf("open knowledge repository: %w", err)
	}
	knowledgeService := knowledge.NewService(knowledgeRepository, assetRepository)
	sessionsRoot := filepath.Join(dataDir, "sessions")
	sessionRepository, err := session.OpenRepository(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("open LLM sessions: %w", err)
	}
	sessionService := session.NewService(sessionRepository, assetRepository)
	runStore, err := llm.OpenRunStore(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("open LLM runs: %w", err)
	}
	llmManager := llm.NewManager(
		configRepository, sessionService, llm.NewAssembler(knowledgeService, assetRepository), provider.Executor{}, runStore,
	)
	exaService := llm.NewExaService(configRepository, sessionService, exa.Client{})

	server := &http.Server{
		Addr: "127.0.0.1:" + strconv.Itoa(runtimeConfig.ListenPort),
		Handler: web.NewHandler(web.Options{
			Version:           version,
			DataDir:           dataDir,
			Config:            runtimeConfig,
			BackendRepository: repository,
			BackendManager:    manager,
			AssetRepository:   assetRepository,
			KnowledgeService:  knowledgeService,
			ConfigRepository:  configRepository,
			SessionService:    sessionService,
			LLMManager:        llmManager,
			ExaService:        exaService,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return &applicationRuntime{server: server, backendManager: manager, llmManager: llmManager}, nil
}

func (runtime *applicationRuntime) shutdownManagers(ctx context.Context) error {
	return errors.Join(runtime.llmManager.Shutdown(ctx), runtime.backendManager.Shutdown(ctx))
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
	runtime, err := newRuntime(dataDir, cfg, options.Version, options.PortOverride)
	if err != nil {
		return err
	}
	server := runtime.server

	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.ListenAndServe()
	}()

	select {
	case err := <-serverResult:
		shutdownContext, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.ShutdownTimeoutSeconds)*time.Second)
		defer cancel()
		managerErr := runtime.shutdownManagers(shutdownContext)
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
		managerErr := runtime.shutdownManagers(shutdownContext)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr, managerErr)
	}
}
