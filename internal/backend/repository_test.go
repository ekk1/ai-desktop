package backend

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestRepositoryPersistsProfileCRUD(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backends", "profiles.json")
	repository, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultProfile()
	profile.Name = "llama"
	profile.Command = "llama-server"

	created, err := repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ID) != 32 {
		t.Fatalf("generated ID length = %d, want 32", len(created.ID))
	}

	created.Description = "local language model"
	updated, err := repository.Update(created.ID, created)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != created.ID || updated.Description != "local language model" {
		t.Fatalf("updated = %#v", updated)
	}

	reopened, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(created.ID)
	if !ok || got.Description != "local language model" {
		t.Fatalf("reopened Get = %#v, %v", got, ok)
	}
	if err := reopened.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get(created.ID); ok {
		t.Fatal("deleted profile is still present")
	}
	if _, err := OpenRepository(path); err != nil {
		t.Fatalf("open after delete: %v", err)
	}
}

func TestOpenRepositoryMigratesVersionOneExecutionToLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backends", "profiles.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema_version":1,"profiles":[{"id":"local-one","name":"Local","command":"echo ok","readiness":{"kind":"none","timeout_seconds":60},"stop_grace_seconds":10,"log_buffer_bytes":1048576}]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	repository, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := repository.Get("local-one")
	if !ok || profile.Execution.Kind != ExecutionLocal {
		t.Fatalf("migrated profile = %#v, found = %v", profile, ok)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		SchemaVersion int       `json:"schema_version"`
		Profiles      []Profile `json:"profiles"`
	}
	if err := json.Unmarshal(contents, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.SchemaVersion != 2 || len(saved.Profiles) != 1 || saved.Profiles[0].Execution.Kind != ExecutionLocal {
		t.Fatalf("saved migration = %#v", saved)
	}
}

func TestRepositoryNormalizesEmptyExecutionToLocal(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultProfile()
	profile.Name = "legacy API input"
	profile.Command = "echo ok"
	profile.Execution = Execution{}
	created, err := repository.Create(profile)
	if err != nil {
		t.Fatal(err)
	}
	if created.Execution.Kind != ExecutionLocal {
		t.Fatalf("execution kind = %q", created.Execution.Kind)
	}
}

func TestRepositoryRejectsDuplicateIDAndMissingRecords(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile := DefaultProfile()
	profile.ID = "fixed"
	profile.Name = "one"
	profile.Command = "run"
	if _, err := repository.Create(profile); err != nil {
		t.Fatal(err)
	}
	profile.Name = "two"
	if _, err := repository.Create(profile); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate Create error = %v", err)
	}
	if _, err := repository.Update("missing", profile); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Update error = %v", err)
	}
	if err := repository.Delete("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing Delete error = %v", err)
	}
}

func TestRepositoryReturnsSortedCopiesDuringConcurrentReads(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "profiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"zeta", "alpha"} {
		profile := DefaultProfile()
		profile.Name = name
		profile.Command = "run"
		profile.Env = map[string]string{"NAME": name}
		if _, err := repository.Create(profile); err != nil {
			t.Fatal(err)
		}
	}

	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			profiles := repository.List()
			if len(profiles) != 2 || profiles[0].Name != "alpha" || profiles[1].Name != "zeta" {
				t.Errorf("List = %#v", profiles)
				return
			}
			profiles[0].Env["NAME"] = "mutated"
		}()
	}
	wait.Wait()

	profiles := repository.List()
	if profiles[0].Env["NAME"] != "alpha" {
		t.Fatalf("repository leaked mutable map: %#v", profiles[0].Env)
	}
}
