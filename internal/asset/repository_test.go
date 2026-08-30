package asset

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportDefaultsToArchiveAndDeduplicatesPhysicalContent(t *testing.T) {
	repository := newTestRepository(t)
	contents := pngFixture(t, 3, 2)

	first, err := repository.Import(ImportInput{
		Reader:      bytes.NewReader(contents),
		DisplayName: "../../first.png",
		MediaType:   "image/png",
		Source:      "upload",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Import(ImportInput{
		Reader:      bytes.NewReader(contents),
		DisplayName: "second.png",
		MediaType:   "image/png",
		Source:      "imagegen",
	})
	if err != nil {
		t.Fatal(err)
	}

	if first.ID == second.ID || first.SHA256 != second.SHA256 {
		t.Fatalf("assets = %#v, %#v", first, second)
	}
	if first.State != StateArchive || first.Width != 3 || first.Height != 2 {
		t.Fatalf("first asset = %#v", first)
	}
	if strings.Contains(first.StoredName, "first") || strings.Contains(first.StoredName, "..") {
		t.Fatalf("unsafe stored name %q", first.StoredName)
	}
	entries, err := os.ReadDir(repository.filesDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("physical files = %d, want 1", len(entries))
	}
}

func TestRepositoryPersistsStateMetadataAndIndependentCopies(t *testing.T) {
	root := t.TempDir()
	indexPath := filepath.Join(root, "index.json")
	filesDir := filepath.Join(root, "files")
	repository, err := OpenRepository(indexPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.Import(ImportInput{Reader: strings.NewReader("memo"), DisplayName: "memo.txt", MediaType: "text/plain", Source: "upload"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.SetState(created.ID, StateActive); err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateMetadata(created.ID, "renamed.txt", "useful")
	if err != nil {
		t.Fatal(err)
	}
	updated.References = append(updated.References, Reference{Module: "bad", RecordID: "mutation"})

	reopened, err := OpenRepository(indexPath, filesDir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopened.Get(created.ID)
	if !ok || got.State != StateActive || got.DisplayName != "renamed.txt" || got.Notes != "useful" || len(got.References) != 0 {
		t.Fatalf("reopened asset = %#v, %v", got, ok)
	}
	active := reopened.List(Filter{State: StateActive, Query: "use"})
	if len(active) != 1 || active[0].ID != created.ID {
		t.Fatalf("filtered assets = %#v", active)
	}
}

func TestDeleteProtectsReferencesAndRemovesLastPhysicalContent(t *testing.T) {
	repository := newTestRepository(t)
	contents := []byte("same bytes")
	first, err := repository.Import(ImportInput{Reader: bytes.NewReader(contents), DisplayName: "one.bin", MediaType: "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.Import(ImportInput{Reader: bytes.NewReader(contents), DisplayName: "two.bin", MediaType: "application/octet-stream"})
	if err != nil {
		t.Fatal(err)
	}
	reference := Reference{Module: "session", RecordID: "panel-1"}
	if _, err := repository.AddReference(first.ID, reference); err != nil {
		t.Fatal(err)
	}
	if err := repository.Delete(first.ID); !errors.Is(err, ErrReferenced) {
		t.Fatalf("Delete referenced error = %v", err)
	}
	if _, err := repository.RemoveReference(first.ID, reference); err != nil {
		t.Fatal(err)
	}
	if err := repository.Delete(first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repository.filesDir, second.StoredName)); err != nil {
		t.Fatalf("shared content removed too early: %v", err)
	}
	if err := repository.Delete(second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repository.filesDir, second.StoredName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("last content still exists: %v", err)
	}
}

func newTestRepository(t *testing.T) *Repository {
	t.Helper()
	root := t.TempDir()
	repository, err := OpenRepository(filepath.Join(root, "index.json"), filepath.Join(root, "files"))
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func pngFixture(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var output bytes.Buffer
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
