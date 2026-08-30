package knowledge

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
)

func TestServiceSynchronizesAssetReferencesAcrossNoteLifecycle(t *testing.T) {
	service, notes, assets := newKnowledgeService(t)
	first := importKnowledgeAsset(t, assets, "first")
	second := importKnowledgeAsset(t, assets, "second")

	created, err := service.Create(Input{Title: "References", AssetIDs: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, first.ID, created.ID, true)
	if err := assets.Delete(first.ID); !errors.Is(err, asset.ErrReferenced) {
		t.Fatalf("referenced asset delete error = %v", err)
	}

	updated, err := service.Update(created.ID, Input{Title: "References", AssetIDs: []string{second.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.AssetIDs) != 1 || updated.AssetIDs[0] != second.ID {
		t.Fatalf("updated note = %#v", updated)
	}
	assertAssetReference(t, assets, first.ID, created.ID, false)
	assertAssetReference(t, assets, second.ID, created.ID, true)

	if err := service.Delete(created.ID); err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, second.ID, created.ID, false)
	if _, ok := notes.Get(created.ID); ok {
		t.Fatal("deleted note remains")
	}
}

func TestServiceRejectsUnknownAssetsWithoutChangingNoteOrReferences(t *testing.T) {
	service, notes, assets := newKnowledgeService(t)
	first := importKnowledgeAsset(t, assets, "first")

	if _, err := service.Create(Input{Title: "Invalid", AssetIDs: []string{"missing"}}); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("create error = %v, want ErrAssetNotFound", err)
	}
	if len(notes.List(Filter{})) != 0 {
		t.Fatal("invalid note was created")
	}

	created, err := service.Create(Input{Title: "Original", AssetIDs: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Update(created.ID, Input{Title: "Changed", AssetIDs: []string{"missing"}}); !errors.Is(err, ErrAssetNotFound) {
		t.Fatalf("update error = %v, want ErrAssetNotFound", err)
	}
	unchanged, ok := notes.Get(created.ID)
	if !ok || unchanged.Title != "Original" || unchanged.AssetIDs[0] != first.ID {
		t.Fatalf("note changed after rejected update: %#v", unchanged)
	}
	assertAssetReference(t, assets, first.ID, created.ID, true)
}

func newKnowledgeService(t *testing.T) (*Service, *Repository, *asset.Repository) {
	t.Helper()
	root := t.TempDir()
	notes, err := OpenRepository(filepath.Join(root, "knowledge", "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	return NewService(notes, assets), notes, assets
}

func importKnowledgeAsset(t *testing.T, repository *asset.Repository, contents string) asset.Asset {
	t.Helper()
	created, err := repository.Import(asset.ImportInput{Reader: bytes.NewBufferString(contents), DisplayName: contents + ".txt", MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func assertAssetReference(t *testing.T, repository *asset.Repository, assetID, noteID string, want bool) {
	t.Helper()
	item, ok := repository.Get(assetID)
	if !ok {
		t.Fatalf("asset %s not found", assetID)
	}
	found := false
	for _, reference := range item.References {
		if reference.Module == "knowledge" && reference.RecordID == noteID {
			found = true
		}
	}
	if found != want {
		t.Fatalf("reference knowledge:%s on %s = %v, want %v", noteID, assetID, found, want)
	}
}
