package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/knowledge"
)

func TestKnowledgeAPICRUDFilteringAndAssetReferences(t *testing.T) {
	handler, notes, assets := newKnowledgeHandler(t)
	linked, err := assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("linked"), DisplayName: "linked.txt", MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}

	createdResponse := doJSON(t, handler, http.MethodPost, "/api/v1/knowledge", knowledge.Input{
		Folder: "models", Title: "Llama flags", Content: "context size", Tags: []string{"local"}, AssetIDs: []string{linked.ID},
	})
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", createdResponse.Code, createdResponse.Body.String())
	}
	var created knowledge.Note
	decodeBody(t, createdResponse, &created)

	listedResponse := doJSON(t, handler, http.MethodGet, "/api/v1/knowledge?folder=models&q=CONTEXT", nil)
	if listedResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", listedResponse.Code, listedResponse.Body.String())
	}
	var listed struct {
		Notes []knowledge.Note `json:"notes"`
	}
	decodeBody(t, listedResponse, &listed)
	if len(listed.Notes) != 1 || listed.Notes[0].ID != created.ID {
		t.Fatalf("listed notes = %#v", listed.Notes)
	}

	updatedResponse := doJSON(t, handler, http.MethodPut, "/api/v1/knowledge/"+created.ID, knowledge.Input{
		Folder: "reference", Title: "Updated flags", Content: "batch size", Tags: []string{"runtime"},
	})
	if updatedResponse.Code != http.StatusOK {
		t.Fatalf("update status = %d: %s", updatedResponse.Code, updatedResponse.Body.String())
	}
	fetchedResponse := doJSON(t, handler, http.MethodGet, "/api/v1/knowledge/"+created.ID, nil)
	var fetched knowledge.Note
	decodeBody(t, fetchedResponse, &fetched)
	if fetched.Title != "Updated flags" || len(fetched.AssetIDs) != 0 {
		t.Fatalf("fetched note = %#v", fetched)
	}
	linkedAfter, _ := assets.Get(linked.ID)
	if len(linkedAfter.References) != 0 {
		t.Fatalf("asset references after update = %#v", linkedAfter.References)
	}

	deletedResponse := doJSON(t, handler, http.MethodDelete, "/api/v1/knowledge/"+created.ID, nil)
	if deletedResponse.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", deletedResponse.Code, deletedResponse.Body.String())
	}
	if len(notes.List(knowledge.Filter{})) != 0 {
		t.Fatal("deleted note remains")
	}
}

func TestKnowledgeAPIUsesErrorEnvelopeForInvalidRequests(t *testing.T) {
	handler, _, _ := newKnowledgeHandler(t)

	invalidJSON := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/knowledge", bytes.NewBufferString(`{"title":`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(invalidJSON, request)
	assertAPIError(t, invalidJSON, http.StatusBadRequest, "invalid_json")

	unknown := doJSON(t, handler, http.MethodGet, "/api/v1/knowledge/missing", nil)
	assertAPIError(t, unknown, http.StatusNotFound, "not_found")

	missingAsset := doJSON(t, handler, http.MethodPost, "/api/v1/knowledge", knowledge.Input{Title: "Invalid", AssetIDs: []string{"missing"}})
	assertAPIError(t, missingAsset, http.StatusBadRequest, "invalid_reference")
}

func newKnowledgeHandler(t *testing.T) (http.Handler, *knowledge.Repository, *asset.Repository) {
	t.Helper()
	root := t.TempDir()
	notes, err := knowledge.OpenRepository(filepath.Join(root, "knowledge", "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	return NewHandler(Options{
		Version: "test", DataDir: root, Config: config.Default(), AssetRepository: assets,
		KnowledgeService: knowledge.NewService(notes, assets),
	}), notes, assets
}

func assertAPIError(t *testing.T, recorder *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d: %s", recorder.Code, status, recorder.Body.String())
	}
	var body errorEnvelope
	decodeBody(t, recorder, &body)
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q", body.Error.Code, code)
	}
}
