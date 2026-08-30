package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
)

func TestServiceSynchronizesPanelAssetReferences(t *testing.T) {
	service, assets := newSessionServiceFixture(t)
	first := importAssetFixture(t, assets, "first")
	second := importAssetFixture(t, assets, "second")
	workspace, err := service.CreateSession(CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	panel, err := service.CreatePanel(workspace.Session.ID, CreatePanelInput{
		ParentID: root.ID, Title: "P", Included: true, AssetIDs: []string{first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPanelReference(t, assets, first.ID, panel.ID, true)

	updated, err := service.UpdatePanel(workspace.Session.ID, panel.ID, UpdatePanelInput{
		Title: "P", Included: true, AssetIDs: []string{second.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertPanelReference(t, assets, first.ID, panel.ID, false)
	assertPanelReference(t, assets, second.ID, panel.ID, true)

	if err := service.DeletePanel(workspace.Session.ID, panel.ID); err != nil {
		t.Fatal(err)
	}
	assertPanelReference(t, assets, second.ID, updated.ID, false)
}

func TestServiceRejectsUnknownAssetWithoutChangingWorkspace(t *testing.T) {
	service, _ := newSessionServiceFixture(t)
	workspace, err := service.CreateSession(CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	if _, err := service.UpdatePanel(workspace.Session.ID, root.ID, UpdatePanelInput{
		Title: "changed", Included: true, AssetIDs: []string{"missing"},
	}); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("unknown asset error = %v", err)
	}
	unchanged, exists := service.Get(workspace.Session.ID)
	if !exists {
		t.Fatal("workspace disappeared")
	}
	if unchanged.Panels[0].Title != root.Title || len(unchanged.Panels[0].AssetIDs) != 0 || len(unchanged.Panels[0].Revisions) != 0 {
		t.Fatalf("workspace changed after validation failure: %#v", unchanged)
	}
}

func TestServiceRestoresWorkspaceWhenReferencePersistenceFails(t *testing.T) {
	rootDirectory := t.TempDir()
	assetDirectory := filepath.Join(rootDirectory, "assets")
	assets, err := asset.OpenRepository(filepath.Join(assetDirectory, "index.json"), filepath.Join(assetDirectory, "files"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenRepository(filepath.Join(rootDirectory, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(repository, assets)
	item := importAssetFixture(t, assets, "rollback")
	workspace, err := service.CreateSession(CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	panel, err := service.CreatePanel(workspace.Session.ID, CreatePanelInput{
		ParentID: root.ID, Title: "before", Included: true, AssetIDs: []string{item.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(assetDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(assetDirectory, []byte("block asset JSON directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdatePanel(workspace.Session.ID, panel.ID, UpdatePanelInput{
		Title: "after", Included: true, AssetIDs: []string{},
	}); err == nil {
		t.Fatal("UpdatePanel succeeded while reference storage was unavailable")
	}

	got, exists := service.Get(workspace.Session.ID)
	if !exists {
		t.Fatal("workspace disappeared")
	}
	index := panelIndex(got.Panels, panel.ID)
	if index < 0 || got.Panels[index].Title != "before" || len(got.Panels[index].AssetIDs) != 1 || got.Panels[index].AssetIDs[0] != item.ID {
		t.Fatalf("workspace was not restored: %#v", got)
	}
	assertPanelReference(t, assets, item.ID, panel.ID, true)
}

func TestServiceSynchronizesSubtreeForkAndRevisionReferences(t *testing.T) {
	service, assets := newSessionServiceFixture(t)
	first := importAssetFixture(t, assets, "first")
	second := importAssetFixture(t, assets, "second")
	workspace, err := service.CreateSession(CreateSessionInput{Title: "source"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	child, err := service.CreatePanel(workspace.Session.ID, CreatePanelInput{
		ParentID: root.ID, Title: "child", Included: true, AssetIDs: []string{first.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := service.CreatePanel(workspace.Session.ID, CreatePanelInput{
		ParentID: child.ID, Title: "grandchild", Included: true, AssetIDs: []string{second.ID},
	})
	if err != nil {
		t.Fatal(err)
	}

	forked, err := service.ForkSession(workspace.Session.ID, ForkSessionInput{PanelID: grandchild.ID, Title: "fork"})
	if err != nil {
		t.Fatal(err)
	}
	assertPanelReference(t, assets, first.ID, forked.Panels[1].ID, true)
	assertPanelReference(t, assets, second.ID, forked.Panels[2].ID, true)

	if err := service.DeletePanel(workspace.Session.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	assertPanelReference(t, assets, first.ID, child.ID, false)
	assertPanelReference(t, assets, second.ID, grandchild.ID, false)
	assertPanelReference(t, assets, first.ID, forked.Panels[1].ID, true)
	assertPanelReference(t, assets, second.ID, forked.Panels[2].ID, true)

	forkRoot := forked.Panels[0]
	withFirst, err := service.UpdatePanel(forked.Session.ID, forkRoot.ID, UpdatePanelInput{Included: true, AssetIDs: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	withSecond, err := service.UpdatePanel(forked.Session.ID, forkRoot.ID, UpdatePanelInput{Included: true, AssetIDs: []string{second.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(withSecond.Revisions) != 2 || withSecond.Revisions[1].AssetIDs[0] != first.ID {
		t.Fatalf("asset revisions = %#v", withSecond.Revisions)
	}
	restored, err := service.RestoreRevision(forked.Session.ID, forkRoot.ID, withSecond.Revisions[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.AssetIDs[0] != first.ID || withFirst.ID != restored.ID {
		t.Fatalf("restored assets = %#v", restored.AssetIDs)
	}
	assertPanelReference(t, assets, first.ID, forkRoot.ID, true)
	assertPanelReference(t, assets, second.ID, forkRoot.ID, false)
	if err := service.DeleteSession(forked.Session.ID); err != nil {
		t.Fatal(err)
	}
	assertPanelReference(t, assets, first.ID, forkRoot.ID, false)
	assertPanelReference(t, assets, first.ID, forked.Panels[1].ID, false)
	assertPanelReference(t, assets, second.ID, forked.Panels[2].ID, false)
	if _, exists := service.Get(forked.Session.ID); exists {
		t.Fatal("deleted session still exists")
	}
}

func newSessionServiceFixture(t *testing.T) (*Service, *asset.Repository) {
	t.Helper()
	root := t.TempDir()
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := OpenRepository(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	return NewService(repository, assets), assets
}

func importAssetFixture(t *testing.T, repository *asset.Repository, name string) asset.Asset {
	t.Helper()
	created, err := repository.Import(asset.ImportInput{
		Reader: strings.NewReader(name), DisplayName: name + ".txt", MediaType: "text/plain", Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func assertPanelReference(t *testing.T, repository *asset.Repository, assetID, panelID string, want bool) {
	t.Helper()
	item, exists := repository.Get(assetID)
	if !exists {
		t.Fatalf("asset %q is missing", assetID)
	}
	got := false
	for _, reference := range item.References {
		if reference == (asset.Reference{Module: panelReferenceModule, RecordID: panelID}) {
			got = true
			break
		}
	}
	if got != want {
		t.Fatalf("asset %q panel %q reference = %v, want %v; references = %#v", assetID, panelID, got, want, item.References)
	}
}
