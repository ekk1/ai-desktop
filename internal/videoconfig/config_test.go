package videoconfig

import (
	"bytes"
	"testing"
)

func TestDefaultConfigContainsHTTPVideoProvider(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(cfg.HTTPProviders) != 1 {
		t.Fatalf("HTTP provider count = %d, want 1", len(cfg.HTTPProviders))
	}
	got := cfg.HTTPProviders[0]
	if got.ID != "sdcpp-video-local" || got.BaseURL != "http://127.0.0.1:1234" || got.MaxConcurrentJobs != 1 {
		t.Fatal(got)
	}
}

func TestConfigRejectsEscapingCLIOutputAndUnsafeHeaders(t *testing.T) {
	cfg := Default()
	cfg.CLIPresets = []CLIPreset{validCLIPreset()}
	cfg.CLIPresets[0].OutputRelativePath = "../result.webm"
	if err := cfg.Validate(); err == nil {
		t.Fatal("escaping output accepted")
	}

	cfg = Default()
	cfg.HTTPProviders[0].Headers["X-Test"] = "ok\nInjected: yes"
	if err := cfg.Validate(); err == nil {
		t.Fatal("header injection accepted")
	}
}

func TestDefaultHTTPParamsReturnsIndependentCopy(t *testing.T) {
	first := DefaultHTTPParams()
	first[0] = 'x'
	if got, want := DefaultHTTPParams(), []byte(`{"width":832,"height":480,"seed":-1,"output_format":"webm","sample_params":{"sample_steps":28}}`); !bytes.Equal(got, want) {
		t.Fatalf("DefaultHTTPParams() = %s, want %s", got, want)
	}
}

func TestConfigCloneDoesNotAliasMutableFields(t *testing.T) {
	original := Default()
	original.HTTPProviders[0].DefaultParams = DefaultHTTPParams()
	original.HTTPProviders[0].Headers["X-Test"] = "before"
	original.CLIPresets = []CLIPreset{validCLIPreset()}
	clone := original.Clone()
	clone.HTTPProviders[0].Headers["X-Test"] = "after"
	clone.HTTPProviders[0].DefaultParams[0] = 'x'
	clone.CLIPresets[0].Env["X_TEST"] = "after"
	clone.CLIPresets[0].DefaultParams[0] = 'x'

	if original.HTTPProviders[0].Headers["X-Test"] != "before" || original.CLIPresets[0].Env["X_TEST"] != "before" || original.HTTPProviders[0].DefaultParams[0] == 'x' || original.CLIPresets[0].DefaultParams[0] == 'x' {
		t.Fatalf("Clone aliased mutable fields: %#v", original)
	}
}

func TestConfigClonePreservesNilCollections(t *testing.T) {
	clone := (Config{}).Clone()
	if clone.HTTPProviders != nil || clone.CLIPresets != nil || clone.TailFramePresets != nil {
		t.Fatalf("Clone changed nil collections: %#v", clone)
	}
}

func TestConfigClonePreservesNilMaps(t *testing.T) {
	original := Config{
		HTTPProviders: []HTTPProvider{{Headers: nil}},
		CLIPresets:    []CLIPreset{{Env: nil}},
	}
	clone := original.Clone()
	if clone.HTTPProviders[0].Headers != nil || clone.CLIPresets[0].Env != nil {
		t.Fatalf("Clone changed nil maps: %#v", clone)
	}
}

func validCLIPreset() CLIPreset {
	return CLIPreset{
		ID: "local-cli", Name: "Local CLI", Enabled: true, ExecutionKind: ExecutionLocalCLI,
		CommandTemplate: "generate-video", WorkDir: "/tmp", Env: map[string]string{"X_TEST": "before"},
		TimeoutSeconds: 60, StopGraceSeconds: 5, LogBufferBytes: 1024,
		OutputRelativePath: "outputs/result.webm", OutputMediaType: "video/webm", OutputExtension: ".webm", MaxOutputBytes: 1,
		DefaultParams: []byte(`{}`),
	}
}
