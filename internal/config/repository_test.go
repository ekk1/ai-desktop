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

func TestRepositoryUpdateImagesPersistsDeepCopy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	repository, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	images := repository.Snapshot().Images
	images.Providers[0].Name = "GPU Image"
	updated, err := repository.UpdateImages(images)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Images.Providers[0].Name != "GPU Image" {
		t.Fatalf("updated = %#v", updated.Images)
	}
	updated.Images.Providers[0].Headers["X-Late"] = "mutation"
	images.Providers[0].Headers["X-Input"] = "mutation"
	if got := repository.Snapshot().Images.Providers[0].Headers; got["X-Late"] != "" || got["X-Input"] != "" {
		t.Fatalf("repository leaked mutable state: %#v", got)
	}
	reopened, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot().Images.Providers[0].Name; got != "GPU Image" {
		t.Fatalf("persisted name = %q", got)
	}
}

func TestRepositoryRejectsInvalidImagesWithoutChangingState(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	before := repository.Snapshot()
	invalid := before.Images.Clone()
	invalid.Providers[0].PollIntervalMilliseconds = 1
	if _, err := repository.UpdateImages(invalid); err == nil {
		t.Fatal("UpdateImages accepted invalid config")
	}
	if got := repository.Snapshot().Images.Providers[0].PollIntervalMilliseconds; got != before.Images.Providers[0].PollIntervalMilliseconds {
		t.Fatalf("poll interval changed to %d", got)
	}
}

func TestRepositoryUpdateVideosPersistsDeepCopy(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	videos := repository.Snapshot().Videos
	videos.HTTPProviders[0].Headers["X-Test"] = "before"
	if _, err := repository.UpdateVideos(videos); err != nil {
		t.Fatal(err)
	}
	videos.HTTPProviders[0].Headers["X-Test"] = "after"
	if got := repository.Snapshot().Videos.HTTPProviders[0].Headers["X-Test"]; got != "before" {
		t.Fatalf("repository retained alias with value %q", got)
	}
}
