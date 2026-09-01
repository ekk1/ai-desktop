package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/store"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

const CurrentSchemaVersion = 4

const (
	defaultListenPort                   = 8188
	defaultShutdownTimeoutSeconds       = 10
	defaultMaxUploadBytes         int64 = 256 << 20
	minimumMaxUploadBytes         int64 = 1 << 20
	maximumMaxUploadBytes         int64 = 16 << 30
)

type Config struct {
	SchemaVersion          int                `json:"schema_version"`
	ListenPort             int                `json:"listen_port"`
	ShutdownTimeoutSeconds int                `json:"shutdown_timeout_seconds"`
	MaxUploadBytes         int64              `json:"max_upload_bytes"`
	LLM                    provider.LLMConfig `json:"llm"`
	Images                 sdcpp.ImageConfig  `json:"images"`
	Videos                 videoconfig.Config `json:"videos"`
}

func Default() Config {
	return Config{
		SchemaVersion:          CurrentSchemaVersion,
		ListenPort:             defaultListenPort,
		ShutdownTimeoutSeconds: defaultShutdownTimeoutSeconds,
		MaxUploadBytes:         defaultMaxUploadBytes,
		LLM:                    provider.DefaultLLMConfig(),
		Images:                 sdcpp.DefaultImageConfig(),
		Videos:                 videoconfig.Default(),
	}
}

func (cfg Config) Clone() Config {
	clone := cfg
	clone.LLM = cfg.LLM.Clone()
	clone.Images = cfg.Images.Clone()
	clone.Videos = cfg.Videos.Clone()
	return clone
}

func ResolveDataDir(explicit string) (string, error) {
	path := explicit
	if path == "" {
		if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
			path = filepath.Join(xdg, "ai-workbench")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve user home directory: %w", err)
			}
			path = filepath.Join(home, ".local", "share", "ai-workbench")
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve data directory %q: %w", path, err)
	}
	return filepath.Clean(absolute), nil
}

func Load(path string) (Config, error) {
	var cfg Config
	err := store.ReadJSONWithBackup(path, &cfg, 0o600, func() error {
		candidate := cfg
		if candidate.SchemaVersion > CurrentSchemaVersion {
			return fmt.Errorf("config schema version %d is newer than supported version %d", candidate.SchemaVersion, CurrentSchemaVersion)
		}
		if candidate.SchemaVersion < CurrentSchemaVersion {
			var err error
			candidate, err = migrate(candidate)
			if err != nil {
				return err
			}
		}
		if err := candidate.Validate(); err != nil {
			return fmt.Errorf("validate config %q: %w", path, err)
		}
		return nil
	})
	if errors.Is(unwrapPathError(err), os.ErrNotExist) {
		cfg = Default()
		if err := Save(path, cfg); err != nil {
			return Config{}, err
		}
		return cfg, nil
	}
	if err != nil {
		return Config{}, err
	}
	if cfg.SchemaVersion > CurrentSchemaVersion {
		return Config{}, fmt.Errorf("config schema version %d is newer than supported version %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
	if cfg.SchemaVersion < CurrentSchemaVersion {
		var migrateErr error
		cfg, migrateErr = migrate(cfg)
		if migrateErr != nil {
			return Config{}, migrateErr
		}
		if err := Save(path, cfg); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("validate config before save: %w", err)
	}
	if err := store.WriteJSON(path, cfg, 0o600); err != nil {
		return fmt.Errorf("save config: %w", err)
	}
	return nil
}

func (cfg Config) Validate() error {
	if cfg.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schema_version must be %d", CurrentSchemaVersion)
	}
	if cfg.ListenPort < 1 || cfg.ListenPort > 65535 {
		return fmt.Errorf("listen_port must be between 1 and 65535")
	}
	if cfg.ShutdownTimeoutSeconds < 1 || cfg.ShutdownTimeoutSeconds > 300 {
		return fmt.Errorf("shutdown_timeout_seconds must be between 1 and 300")
	}
	if cfg.MaxUploadBytes < minimumMaxUploadBytes || cfg.MaxUploadBytes > maximumMaxUploadBytes {
		return fmt.Errorf("max_upload_bytes must be between %d and %d", minimumMaxUploadBytes, maximumMaxUploadBytes)
	}
	if err := cfg.LLM.Validate(); err != nil {
		return fmt.Errorf("llm: %w", err)
	}
	if err := cfg.Images.Validate(); err != nil {
		return fmt.Errorf("images: %w", err)
	}
	if err := cfg.Videos.Validate(); err != nil {
		return fmt.Errorf("videos: %w", err)
	}
	return nil
}

func migrate(cfg Config) (Config, error) {
	for cfg.SchemaVersion < CurrentSchemaVersion {
		switch cfg.SchemaVersion {
		case 1:
			cfg.SchemaVersion = 2
			cfg.LLM = provider.DefaultLLMConfig()
		case 2:
			cfg.SchemaVersion = 3
			cfg.Images = sdcpp.DefaultImageConfig()
		case 3:
			cfg.SchemaVersion = 4
			cfg.Videos = videoconfig.Default()
		default:
			return Config{}, fmt.Errorf("no migration path from config schema version %d", cfg.SchemaVersion)
		}
	}
	return cfg, nil
}

func unwrapPathError(err error) error {
	for err != nil {
		var pathErr *os.PathError
		if errors.As(err, &pathErr) {
			return pathErr.Err
		}
		err = errors.Unwrap(err)
	}
	return nil
}
