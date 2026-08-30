package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryUpdateLLMPersistsWithPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}

	llm := repository.Snapshot().LLM
	llm.Exa.APIKey = "exa-test"
	updated, err := repository.UpdateLLM(llm)
	if err != nil {
		t.Fatal(err)
	}
	if updated.LLM.Exa.APIKey != "exa-test" {
		t.Fatalf("updated config = %#v", updated)
	}

	// A returned snapshot must not mutate repository state.
	updated.LLM.Exa.APIKey = "mutated-copy"
	updated.LLM.Providers[0].Headers["X-Mutated"] = "yes"
	if got := repository.Snapshot(); got.LLM.Exa.APIKey != "exa-test" || got.LLM.Providers[0].Headers["X-Mutated"] != "" {
		t.Fatalf("repository leaked mutable state: %#v", got.LLM)
	}

	reopened, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().LLM.Exa.APIKey; got != "exa-test" {
		t.Fatalf("persisted Exa key = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestRepositoryRejectsInvalidLLMWithoutChangingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	before := repository.Snapshot()
	wantProviderID := before.LLM.QuickPaths[0].ProviderID
	invalid := before.LLM.Clone()
	invalid.QuickPaths[0].ProviderID = "missing"
	if _, err := repository.UpdateLLM(invalid); err == nil {
		t.Fatal("UpdateLLM accepted invalid config")
	}
	if got := repository.Snapshot().LLM.QuickPaths[0].ProviderID; got != wantProviderID {
		t.Fatalf("provider reference changed to %q", got)
	}
}
