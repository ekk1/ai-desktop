package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

func TestResolveDataDirMakesExplicitPathAbsolute(t *testing.T) {
	root := t.TempDir()
	oldWorkingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWorkingDir) })

	got, err := ResolveDataDir("workspace-data")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "workspace-data")
	if got != want {
		t.Fatalf("ResolveDataDir = %q, want %q", got, want)
	}
}

func TestResolveDataDirUsesXDGDataHome(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdg)

	got, err := ResolveDataDir("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(xdg, "ai-workbench")
	if got != want {
		t.Fatalf("ResolveDataDir = %q, want %q", got, want)
	}
}

func TestLoadCreatesDefaultConfigWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := Default()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("mode = %o, want 600", gotMode)
	}
}

func TestConfigValidateRejectsOutOfRangeValues(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Config)
	}{
		{name: "zero port", change: func(c *Config) { c.ListenPort = 0 }},
		{name: "large port", change: func(c *Config) { c.ListenPort = 65536 }},
		{name: "zero shutdown timeout", change: func(c *Config) { c.ShutdownTimeoutSeconds = 0 }},
		{name: "large shutdown timeout", change: func(c *Config) { c.ShutdownTimeoutSeconds = 301 }},
		{name: "small upload limit", change: func(c *Config) { c.MaxUploadBytes = (1 << 20) - 1 }},
		{name: "large upload limit", change: func(c *Config) { c.MaxUploadBytes = (16 << 30) + 1 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := Default()
			test.change(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate accepted invalid config")
			}
		})
	}
}

func TestLoadRejectsFutureSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents := []byte("{\"schema_version\":99,\"listen_port\":8188,\"shutdown_timeout_seconds\":10,\"max_upload_bytes\":268435456}\n")
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a future schema version")
	}
}

func TestLoadMigratesSchemaOneAndPreservesRuntimeFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := []byte(`{"schema_version":1,"listen_port":9001,"shutdown_timeout_seconds":15,"max_upload_bytes":1048576}`)
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != CurrentSchemaVersion || got.ListenPort != 9001 || got.ShutdownTimeoutSeconds != 15 || got.MaxUploadBytes != 1048576 {
		t.Fatalf("migrated runtime config = %#v", got)
	}
	if len(got.LLM.QuickPaths) != 1 || got.LLM.QuickPaths[0].Name != "Local" {
		t.Fatalf("migrated LLM config = %#v", got.LLM)
	}
	if len(got.Images.Providers) != 1 || got.Images.Providers[0].ID != "sdcpp-local" {
		t.Fatalf("migrated image config = %#v", got.Images)
	}

	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	reloadedJSON, err := json.Marshal(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reloadedJSON, gotJSON) {
		t.Fatalf("persisted config = %#v, want %#v", reloaded, got)
	}
}

func TestLoadMigratesSchemaTwoWithImageDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	contents, err := json.Marshal(Default())
	if err != nil {
		t.Fatal(err)
	}
	var old map[string]any
	if err := json.Unmarshal(contents, &old); err != nil {
		t.Fatal(err)
	}
	old["schema_version"] = float64(2)
	delete(old, "images")
	contents, err = json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != CurrentSchemaVersion || len(got.Images.Providers) != 1 || got.Images.Providers[0].ID != "sdcpp-local" {
		t.Fatalf("migrated config = %#v", got)
	}
}

func TestLoadMigratesSchemaThreeWithVideoDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	old := Default()
	old.SchemaVersion = 3
	old.Videos = videoconfig.Config{}
	contents, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 4 || len(got.Videos.HTTPProviders) != 1 || got.Videos.HTTPProviders[0].ID != "sdcpp-video-local" {
		t.Fatalf("migrated config = %#v", got)
	}
}
