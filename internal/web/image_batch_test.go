package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/imagegen"
)

func TestImageBatchAPIProvidesCRUDFilteringAndAssetSummaries(t *testing.T) {
	fixture := newImageBatchWebFixture(t)
	input := importWebImage(t, fixture.assets, "input.png")
	created := fixture.request(http.MethodPost, "/api/v1/images/batches", []byte(`{"title":"Draw","folder":"ideas","provider_id":"sdcpp-local","concurrency":2,"base_params":{"width":768}}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var batch imagegen.Batch
	if err := json.NewDecoder(created.Body).Decode(&batch); err != nil {
		t.Fatal(err)
	}
	itemsBody, _ := json.Marshal(map[string]any{"items": []any{
		map[string]any{"prompt": "one", "input_assets": map[string]any{"init_image_id": input.ID}},
		map[string]any{"prompt": "two"},
	}})
	itemsResponse := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/items", itemsBody)
	if itemsResponse.Code != http.StatusCreated {
		t.Fatalf("items status = %d, body = %s", itemsResponse.Code, itemsResponse.Body.String())
	}
	var createdItems struct {
		Items []imagegen.Item `json:"items"`
	}
	if err := json.NewDecoder(itemsResponse.Body).Decode(&createdItems); err != nil || len(createdItems.Items) != 2 {
		t.Fatalf("items = %#v, error = %v", createdItems, err)
	}
	if _, err := fixture.assets.SetState(input.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}

	detail := fixture.request(http.MethodGet, "/api/v1/images/batches/"+batch.ID, nil)
	var got imageBatchResponse
	if err := json.NewDecoder(detail.Body).Decode(&got); detail.Code != http.StatusOK || err != nil {
		t.Fatalf("detail status = %d, body = %s, error = %v", detail.Code, detail.Body.String(), err)
	}
	if !got.ProviderAvailable || len(got.Batch.Items) != 2 || got.Batch.Items[0].Order != 0 || len(got.Assets) != 1 || got.Assets[0].State != asset.StateArchive {
		t.Fatalf("detail = %#v", got)
	}

	listed := fixture.request(http.MethodGet, "/api/v1/images/batches?folder=ideas&q=draw", nil)
	var collection struct {
		Batches []imagegen.Batch `json:"batches"`
	}
	if err := json.NewDecoder(listed.Body).Decode(&collection); listed.Code != http.StatusOK || err != nil || len(collection.Batches) != 1 {
		t.Fatalf("list status = %d, body = %s, error = %v", listed.Code, listed.Body.String(), err)
	}

	move := fixture.request(http.MethodPost, "/api/v1/images/batches/"+batch.ID+"/items/"+createdItems.Items[1].ID+"/move", []byte(`{"direction":-1}`))
	if move.Code != http.StatusOK {
		t.Fatalf("move status = %d, body = %s", move.Code, move.Body.String())
	}
	updated := fixture.request(http.MethodPut, "/api/v1/images/batches/"+batch.ID, []byte(`{"title":"Final","folder":"done","provider_id":"sdcpp-local","concurrency":1,"base_params":{}}`))
	if updated.Code != http.StatusOK {
		t.Fatalf("update status = %d, body = %s", updated.Code, updated.Body.String())
	}
	deletedItem := fixture.request(http.MethodDelete, "/api/v1/images/batches/"+batch.ID+"/items/"+createdItems.Items[0].ID, nil)
	if deletedItem.Code != http.StatusNoContent {
		t.Fatalf("delete item status = %d, body = %s", deletedItem.Code, deletedItem.Body.String())
	}
	deletedBatch := fixture.request(http.MethodDelete, "/api/v1/images/batches/"+batch.ID, nil)
	if deletedBatch.Code != http.StatusNoContent {
		t.Fatalf("delete batch status = %d, body = %s", deletedBatch.Code, deletedBatch.Body.String())
	}
}

func TestImageBatchAPIRejectsUnknownFieldsAndInvalidIDs(t *testing.T) {
	fixture := newImageBatchWebFixture(t)
	response := fixture.request(http.MethodPost, "/api/v1/images/batches", []byte(`{"title":"Draw","provider_id":"sdcpp-local","concurrency":1,"base_params":{},"unknown":true}`))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %s", response.Code, response.Body.String())
	}
	response = fixture.request(http.MethodGet, "/api/v1/images/batches/not-an-id", nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("invalid ID status = %d, body = %s", response.Code, response.Body.String())
	}
}

type imageBatchWebFixture struct {
	handler http.Handler
	service *imagegen.Service
	assets  *asset.Repository
	config  *config.Repository
}

func newImageBatchWebFixture(t *testing.T) imageBatchWebFixture {
	t.Helper()
	root := t.TempDir()
	configuration, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := imagegen.OpenRepository(filepath.Join(root, "images", "batches"))
	if err != nil {
		t.Fatal(err)
	}
	service := imagegen.NewService(repository, assets)
	handler := NewHandler(Options{
		Version: "test", Config: configuration.Snapshot(), ConfigRepository: configuration,
		AssetRepository: assets, ImageService: service,
	})
	return imageBatchWebFixture{handler: handler, service: service, assets: assets, config: configuration}
}

func (fixture imageBatchWebFixture) request(method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	response := httptest.NewRecorder()
	fixture.handler.ServeHTTP(response, request)
	return response
}

func importWebImage(t *testing.T, repository *asset.Repository, name string) asset.Asset {
	t.Helper()
	created, err := repository.Import(asset.ImportInput{Reader: bytes.NewReader(tinyPNG), DisplayName: name, MediaType: "image/png", Source: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

var tinyPNG = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
