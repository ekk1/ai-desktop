package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/imagegen"
	"github.com/ekk1/ai-desktop/internal/instance"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/llm"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/session"
	"github.com/ekk1/ai-desktop/internal/videogen"
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
	imageManager   *imagegen.Manager
	videoManager   *videogen.Manager
	tailExtractor  *videogen.TailExtractor
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
	imageRepository, err := imagegen.OpenRepository(filepath.Join(dataDir, "images", "batches"))
	if err != nil {
		return nil, fmt.Errorf("open image batches: %w", err)
	}
	imageService := imagegen.NewService(imageRepository, assetRepository)
	imageClient := sdcpp.Client{}
	imageManager := imagegen.NewManager(configRepository, imageService, imagegen.NewAssembler(assetRepository), assetRepository, imageClient)
	videoRepository, err := videogen.OpenRepository(filepath.Join(dataDir, "videos", "batches"))
	if err != nil {
		return nil, fmt.Errorf("open video batches: %w", err)
	}
	tailRepository, err := videogen.OpenTailRepository(filepath.Join(dataDir, "videos", "tail-extractions.json"))
	if err != nil {
		return nil, fmt.Errorf("open tail extractions: %w", err)
	}
	workspaceRoot := filepath.Join(dataDir, "video-workspace")
	if err := os.MkdirAll(workspaceRoot, 0o700); err != nil {
		return nil, fmt.Errorf("open video workspace: %w", err)
	}
	if err := os.Chmod(workspaceRoot, 0o700); err != nil {
		return nil, fmt.Errorf("protect video workspace: %w", err)
	}
	videoService := videogen.NewService(videoRepository, assetRepository)
	cliExecutor := videogen.NewCLIExecutor()
	videoManager := videogen.NewManager(configRepository, videoService, videogen.NewHTTPAssembler(assetRepository), sdcpp.VideoClient{}, videogen.NewWorkspaceManager(workspaceRoot, assetRepository), cliExecutor, assetRepository)
	tailExtractor := videogen.NewTailExtractor(configRepository, tailRepository, assetRepository, cliExecutor, filepath.Join(dataDir, "videos", "tail-workspaces"), filepath.Join(dataDir, "videos", "tail-logs"))
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
			ImageCapabilities: imageClient,
			ImageService:      imageService,
			ImageManager:      imageManager,
			VideoCapabilities: imageClient,
			VideoService:      videoService,
			VideoManager:      videoManager,
			TailExtractor:     tailExtractor,
			TailRepository:    tailRepository,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return &applicationRuntime{server: server, backendManager: manager, llmManager: llmManager, imageManager: imageManager, videoManager: videoManager, tailExtractor: tailExtractor}, nil
}

func (runtime *applicationRuntime) shutdownManagers(ctx context.Context) error {
	shutdowns := []func(context.Context) error{
		runtime.imageManager.Shutdown,
		runtime.llmManager.Shutdown,
		runtime.backendManager.Shutdown,
		runtime.videoManager.Shutdown,
		runtime.tailExtractor.Shutdown,
	}
	errorsFound := make(chan error, len(shutdowns))
	var wait sync.WaitGroup
	for _, shutdown := range shutdowns {
		wait.Add(1)
		go func(shutdown func(context.Context) error) {
			defer wait.Done()
			if err := shutdown(ctx); err != nil {
				errorsFound <- err
			}
		}(shutdown)
	}
	wait.Wait()
	close(errorsFound)
	var failures []error
	for err := range errorsFound {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
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
		shutdownResult := make(chan error, 1)
		managerResult := make(chan error, 1)
		go func() { shutdownResult <- server.Shutdown(shutdownContext) }()
		go func() { managerResult <- runtime.shutdownManagers(shutdownContext) }()
		shutdownErr := <-shutdownResult
		serveErr := <-serverResult
		managerErr := <-managerResult
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		return errors.Join(shutdownErr, serveErr, managerErr)
	}
}
