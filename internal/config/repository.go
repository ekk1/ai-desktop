package config

import (
	"fmt"
	"sync"

	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
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

	repository.mu.Lock()
	defer repository.mu.Unlock()
	candidate := repository.config.Clone()
	candidate.LLM = candidateLLM
	if err := Save(repository.path, candidate); err != nil {
		return Config{}, err
	}
	repository.config = candidate
	return candidate.Clone(), nil
}

func (repository *Repository) UpdateImages(images sdcpp.ImageConfig) (Config, error) {
	candidateImages := images.Clone()
	if err := candidateImages.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate image config: %w", err)
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	candidate := repository.config.Clone()
	candidate.Images = candidateImages
	if err := Save(repository.path, candidate); err != nil {
		return Config{}, err
	}
	repository.config = candidate
	return candidate.Clone(), nil
}
