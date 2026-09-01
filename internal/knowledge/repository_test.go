package knowledge

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/store"
)

func TestRepositoryPersistsCRUDAndReturnsDeepCopies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge", "notes.json")
	repository, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.Create(Input{
		Folder: "projects/local-ai", Title: "Server flags", Content: "llama-server context settings",
		Tags: []string{"llama", "local", "llama"}, AssetIDs: []string{"asset-a", "asset-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || len(created.Tags) != 2 || len(created.AssetIDs) != 1 {
		t.Fatalf("created note = %#v", created)
	}
	created.Tags[0] = "mutated"
	stored, ok := repository.Get(created.ID)
	if !ok || stored.Tags[0] != "llama" {
		t.Fatalf("stored note changed through returned copy: %#v", stored)
	}

	updated, err := repository.Update(created.ID, Input{
		Folder: "reference", Title: "Updated flags", Content: "context size and batch size",
		Tags: []string{"runtime"}, AssetIDs: []string{"asset-b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CreatedAt != stored.CreatedAt || !updated.UpdatedAt.After(stored.UpdatedAt) {
		t.Fatalf("timestamps before=%s/%s after=%s/%s", stored.CreatedAt, stored.UpdatedAt, updated.CreatedAt, updated.UpdatedAt)
	}

	reopened, err := OpenRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reopened.Get(created.ID)
	if !ok || persisted.Title != "Updated flags" || persisted.Folder != "reference" || persisted.AssetIDs[0] != "asset-b" {
		t.Fatalf("persisted note = %#v", persisted)
	}
	if err := reopened.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Get(created.ID); ok {
		t.Fatal("deleted note remains")
	}
	if err := reopened.Delete(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryFiltersFolderAndTextWithStableOrdering(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.Create(Input{Folder: "models", Title: "Llama", Content: "quantized local model", Tags: []string{"text"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Create(Input{Folder: "images", Title: "Landscape", Content: "wide composition", Tags: []string{"art"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Update(first.ID, Input{Folder: "models", Title: "Llama", Content: "quantized local model", Tags: []string{"text"}}); err != nil {
		t.Fatal(err)
	}

	all := repository.List(Filter{})
	if len(all) != 2 || all[0].ID != first.ID || all[1].ID != second.ID {
		t.Fatalf("ordered notes = %#v", all)
	}
	if got := repository.List(Filter{Folder: "images"}); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("folder filter = %#v", got)
	}
	if got := repository.List(Filter{Query: "QUANTIZED"}); len(got) != 1 || got[0].ID != first.ID {
		t.Fatalf("content filter = %#v", got)
	}
	if got := repository.List(Filter{Query: "art"}); len(got) != 1 || got[0].ID != second.ID {
		t.Fatalf("tag filter = %#v", got)
	}
}

func TestRepositoryRequiresTitleButAllowsEmptyContent(t *testing.T) {
	repository, err := OpenRepository(filepath.Join(t.TempDir(), "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Create(Input{Title: "  "}); err == nil {
		t.Fatal("empty title was accepted")
	}
	created, err := repository.Create(Input{Title: "Reminder", Content: ""})
	if err != nil {
		t.Fatalf("empty content rejected: %v", err)
	}
	if created.Tags == nil || created.AssetIDs == nil {
		t.Fatalf("empty collections = tags %#v, assets %#v; want non-nil empty slices", created.Tags, created.AssetIDs)
	}
}

func TestRepositoryDoesNotRestoreInvalidKnowledgeBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notes.json")
	now := time.Now().UTC()
	invalid := document{SchemaVersion: schemaVersion, Notes: []Note{{
		ID: "not-a-generated-id", Title: "", Tags: []string{}, AssetIDs: []string{}, CreatedAt: now, UpdatedAt: now,
	}}}
	if err := store.WriteJSON(path+".bak", invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(`{"schema_version":1`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRepository(path); err == nil {
		t.Fatal("repository restored an invalid knowledge backup")
	}
	contents, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(contents, corrupt) {
		t.Fatalf("primary = %q, error = %v; want corrupt primary left unchanged", contents, err)
	}
}
