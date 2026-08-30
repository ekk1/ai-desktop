package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
)

func TestAssetBatchStateIsAtomic(t *testing.T) {
	handler, repository := newAssetHandler(t)
	first := uploadAsset(t, handler, "first.txt", "text/plain", []byte("first"))
	second := uploadAsset(t, handler, "second.txt", "text/plain", []byte("second"))

	failed := doJSON(t, handler, http.MethodPost, "/api/v1/assets/state", map[string]any{
		"asset_ids": []string{first.ID, "missing"},
		"state":     "active",
	})
	if failed.Code != http.StatusNotFound {
		t.Fatalf("batch with missing asset status = %d, want 404: %s", failed.Code, failed.Body.String())
	}
	unchanged, ok := repository.Get(first.ID)
	if !ok || unchanged.State != asset.StateArchive {
		t.Fatalf("first asset changed after failed batch: %#v", unchanged)
	}

	response := doJSON(t, handler, http.MethodPost, "/api/v1/assets/state", map[string]any{
		"asset_ids": []string{first.ID, second.ID, first.ID},
		"state":     "active",
	})
	if response.Code != http.StatusOK {
		t.Fatalf("batch state status = %d: %s", response.Code, response.Body.String())
	}
	var body struct {
		Assets []asset.Asset `json:"assets"`
	}
	decodeBody(t, response, &body)
	if len(body.Assets) != 2 || body.Assets[0].State != asset.StateActive || body.Assets[1].State != asset.StateActive {
		t.Fatalf("updated assets = %#v", body.Assets)
	}
}

func TestAssetExportReturnsSelectedFilesAsZIP(t *testing.T) {
	handler, _ := newAssetHandler(t)
	first := uploadAsset(t, handler, "same.txt", "text/plain", []byte("first"))
	second := uploadAsset(t, handler, "same.txt", "text/plain", []byte("second"))

	response := doJSON(t, handler, http.MethodPost, "/api/v1/assets/export", map[string]any{
		"asset_ids": []string{first.ID, second.ID},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/zip" {
		t.Fatalf("Content-Type = %q", got)
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 2 {
		t.Fatalf("zip entries = %d, want 2", len(archive.File))
	}
	if archive.File[0].Name != "same.txt" || archive.File[1].Name != "same-2.txt" {
		t.Fatalf("zip entry names = %q, %q", archive.File[0].Name, archive.File[1].Name)
	}
	for index, want := range []string{"first", "second"} {
		file, err := archive.File[index].Open()
		if err != nil {
			t.Fatal(err)
		}
		var contents bytes.Buffer
		if _, err := contents.ReadFrom(file); err != nil {
			t.Fatal(err)
		}
		_ = file.Close()
		if contents.String() != want {
			t.Fatalf("entry %d = %q, want %q", index, contents.String(), want)
		}
	}
}

func TestAssetExportSanitizesCrossPlatformArchivePaths(t *testing.T) {
	handler, _ := newAssetHandler(t)
	item := uploadAsset(t, handler, `..\outside.txt`, "text/plain", []byte("safe"))

	response := doJSON(t, handler, http.MethodPost, "/api/v1/assets/export", map[string]any{"asset_ids": []string{item.ID}})
	if response.Code != http.StatusOK {
		t.Fatalf("export status = %d: %s", response.Code, response.Body.String())
	}
	archive, err := zip.NewReader(bytes.NewReader(response.Body.Bytes()), int64(response.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != 1 || archive.File[0].Name != "outside.txt" {
		t.Fatalf("zip entries = %#v, want outside.txt", archive.File)
	}
}

func TestAssetUploadStateMetadataAndContent(t *testing.T) {
	handler, repository := newAssetHandler(t)
	contents := webPNGFixture(t)
	upload := uploadAsset(t, handler, "../../portrait.png", "image/png", contents)
	if upload.State != asset.StateArchive || upload.Width != 2 || upload.Height != 1 {
		t.Fatalf("uploaded = %#v", upload)
	}
	if strings.Contains(upload.StoredName, "portrait") || strings.Contains(upload.StoredName, "..") {
		t.Fatalf("unsafe stored name %q", upload.StoredName)
	}

	state := doJSON(t, handler, http.MethodPost, "/api/v1/assets/"+upload.ID+"/state", map[string]string{"state": "active"})
	if state.Code != http.StatusOK {
		t.Fatalf("state status = %d: %s", state.Code, state.Body.String())
	}
	patch := doJSON(t, handler, http.MethodPatch, "/api/v1/assets/"+upload.ID, map[string]string{"display_name": "selected.png", "notes": "favorite"})
	if patch.Code != http.StatusOK {
		t.Fatalf("patch status = %d: %s", patch.Code, patch.Body.String())
	}

	listed := doJSON(t, handler, http.MethodGet, "/api/v1/assets?state=active&q=favorite", nil)
	var listBody struct {
		Assets []asset.Asset `json:"assets"`
	}
	decodeBody(t, listed, &listBody)
	if len(listBody.Assets) != 1 || listBody.Assets[0].DisplayName != "selected.png" {
		t.Fatalf("assets = %#v", listBody.Assets)
	}

	content := httptest.NewRecorder()
	handler.ServeHTTP(content, httptest.NewRequest(http.MethodGet, "/api/v1/assets/"+upload.ID+"/content", nil))
	if content.Code != http.StatusOK || !bytes.Equal(content.Body.Bytes(), contents) {
		t.Fatalf("content = %d, %d bytes", content.Code, content.Body.Len())
	}
	if got := content.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type = %q", got)
	}

	deleted := doJSON(t, handler, http.MethodDelete, "/api/v1/assets/"+upload.ID, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", deleted.Code, deleted.Body.String())
	}
	if len(repository.List(asset.Filter{})) != 0 {
		t.Fatal("deleted asset remains in repository")
	}
}

func TestAssetAPIReportsReferencedDeleteConflict(t *testing.T) {
	handler, repository := newAssetHandler(t)
	upload := uploadAsset(t, handler, "note.txt", "text/plain", []byte("note"))
	if _, err := repository.AddReference(upload.ID, asset.Reference{Module: "knowledge", RecordID: "note-1"}); err != nil {
		t.Fatal(err)
	}
	response := doJSON(t, handler, http.MethodDelete, "/api/v1/assets/"+upload.ID, nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("delete referenced status = %d", response.Code)
	}
}

func newAssetHandler(t *testing.T) (http.Handler, *asset.Repository) {
	t.Helper()
	root := t.TempDir()
	repository, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Options{Version: "test", DataDir: root, Config: config.Default(), AssetRepository: repository}), repository
}

func uploadAsset(t *testing.T, handler http.Handler, name, mediaType string, contents []byte) asset.Asset {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(contents); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("media_type", mediaType); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("source", "upload"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/assets", &body).WithContext(context.Background())
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", recorder.Code, recorder.Body.String())
	}
	var result asset.Asset
	if err := json.NewDecoder(recorder.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func webPNGFixture(t *testing.T) []byte {
	t.Helper()
	var contents bytes.Buffer
	if err := png.Encode(&contents, image.NewRGBA(image.Rect(0, 0, 2, 1))); err != nil {
		t.Fatal(err)
	}
	return contents.Bytes()
}
