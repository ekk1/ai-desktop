package llm

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	jsonstore "github.com/ekk1/ai-desktop/internal/store"
)

func TestRunStoreReopensActiveRunsAsInterrupted(t *testing.T) {
	root := t.TempDir()
	store, err := OpenRunStore(root)
	if err != nil {
		t.Fatal(err)
	}
	original := Run{
		ID: "run-a", SessionID: "session-a", ParentPanelID: "panel-a", QuickPathID: "path-a",
		State: RunRunning, Snapshot: Snapshot{Content: "request"}, CreatedAt: time.Now().UTC(),
	}
	if err := store.Save(original); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenRunStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, exists := reopened.Get("run-a")
	if !exists || got.State != RunInterrupted || got.Error.Code != "interrupted" || got.CompletedAt.IsZero() {
		t.Fatalf("reopened run = %#v, exists = %v", got, exists)
	}
	if _, err := filepath.Abs(filepath.Join(root, "session-a", "runs", "run-a.json")); err != nil {
		t.Fatal(err)
	}
}

func TestRunStoreReturnsDeepCopiesAndNewestFirstSessionList(t *testing.T) {
	store, err := OpenRunStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := Run{
		ID: "run-first", SessionID: "session", State: RunSucceeded, Output: "first",
		Snapshot:  Snapshot{Headers: map[string]string{"X-Test": "value"}, Panels: []PanelSnapshot{{ID: "panel", AssetIDs: []string{"asset"}}}},
		CreatedAt: time.Unix(1, 0).UTC(), CompletedAt: time.Unix(2, 0).UTC(),
	}
	second := Run{ID: "run-second", SessionID: "session", State: RunFailed, CreatedAt: time.Unix(3, 0).UTC()}
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(first.ID)
	got.Snapshot.Headers["X-Test"] = "mutated"
	got.Snapshot.Panels[0].AssetIDs[0] = "mutated"
	unchanged, _ := store.Get(first.ID)
	if unchanged.Snapshot.Headers["X-Test"] != "value" || unchanged.Snapshot.Panels[0].AssetIDs[0] != "asset" {
		t.Fatalf("store leaked mutable state: %#v", unchanged.Snapshot)
	}
	listed := store.List("session")
	if len(listed) != 2 || listed[0].ID != second.ID || listed[1].ID != first.ID {
		t.Fatalf("listed runs = %#v", listed)
	}
}

func TestRunStoreDoesNotRestoreBackupWithDuplicateRunID(t *testing.T) {
	root := t.TempDir()
	runStore, err := OpenRunStore(root)
	if err != nil {
		t.Fatal(err)
	}
	first := Run{ID: "run-duplicate", SessionID: "session-a", State: RunSucceeded, CreatedAt: time.Unix(1, 0).UTC(), CompletedAt: time.Unix(2, 0).UTC()}
	second := first
	second.SessionID = "session-b"
	if err := runStore.Save(first); err != nil {
		t.Fatal(err)
	}
	if err := runStore.Save(second); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, second.SessionID, "runs", second.ID+".json")
	if err := jsonstore.WriteJSON(path+".bak", runDocument{SchemaVersion: runSchemaVersion, Run: second}, 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte(`{"schema_version":1`)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRunStore(root); err == nil {
		t.Fatal("run store accepted duplicate run IDs")
	}
	contents, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(contents, corrupt) {
		t.Fatalf("primary = %q, error = %v; want corrupt primary left unchanged", contents, err)
	}
}
