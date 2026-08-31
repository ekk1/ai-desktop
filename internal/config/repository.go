package config

import (
	"fmt"
	"sync"

	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

type Repository struct {
	mu     sync.RWMutex
	path   string
	config Config
}

func OpenRepository(path string) (*Repository, error) {
	configuration, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Repository{path: path, config: configuration.Clone()}, nil
}

func (repository *Repository) Snapshot() Config {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	return repository.config.Clone()
}

func (repository *Repository) UpdateLLM(llm provider.LLMConfig) (Config, error) {
	candidateLLM := llm.Clone()
	if err := candidateLLM.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate LLM config: %w", err)
	}

	return repository.update(func(cfg *Config) { cfg.LLM = candidateLLM })
}

func (repository *Repository) UpdateImages(images sdcpp.ImageConfig) (Config, error) {
	candidateImages := images.Clone()
	if err := candidateImages.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate image config: %w", err)
	}

	return repository.update(func(cfg *Config) { cfg.Images = candidateImages })
}

func (repository *Repository) UpdateVideos(videos videoconfig.Config) (Config, error) {
	candidateVideos := videos.Clone()
	if err := candidateVideos.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate video config: %w", err)
	}
	return repository.update(func(cfg *Config) { cfg.Videos = candidateVideos })
}

func (repository *Repository) update(change func(*Config)) (Config, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	candidate := repository.config.Clone()
	change(&candidate)
	if err := Save(repository.path, candidate); err != nil {
		return Config{}, err
	}
	repository.config = candidate
	return candidate.Clone(), nil
}
