package web

import (
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
