package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/videogen"
)

func TestVideoBatchAndItemAPIContract(t *testing.T) {
	fixture := newVideoAttemptFixture(t)
	defer fixture.manager.Shutdown(context.Background())

	createBatch := func(title, folder string) videogen.Batch {
		t.Helper()
		response := fixture.request(http.MethodPost, "/api/v1/videos/batches", []byte(`{"title":"`+title+`","folder":"`+folder+`","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":1,"common_params":{"seed":7},"timing":{"mode":"frames","video_frames":1,"fps":1}}`))
		var batch videogen.Batch
		if err := json.NewDecoder(response.Body).Decode(&batch); response.Code != http.StatusCreated || err != nil {
			t.Fatalf("create %q status=%d body=%s decode=%v", title, response.Code, response.Body.String(), err)
		}
		return batch
	}
	primary := createBatch("alpha contract", "wanted")
	other := createBatch("beta contract", "other")
	strictBatch := fixture.request(http.MethodPost, "/api/v1/videos/batches", []byte(`{"unknown":true}`))
	if strictBatch.Code != http.StatusBadRequest {
		t.Fatalf("batch unknown status=%d body=%s", strictBatch.Code, strictBatch.Body.String())
	}
	strictBatchUpdate := fixture.request(http.MethodPut, "/api/v1/videos/batches/"+primary.ID, []byte(`{"unknown":true}`))
	if strictBatchUpdate.Code != http.StatusBadRequest {
		t.Fatalf("batch update unknown status=%d body=%s", strictBatchUpdate.Code, strictBatchUpdate.Body.String())
	}

	for _, check := range []struct {
		path string
		want string
	}{
		{"/api/v1/videos/batches?q=ALPHA", primary.ID},
		{"/api/v1/videos/batches?folder=other", other.ID},
	} {
		response := fixture.request(http.MethodGet, check.path, nil)
		var listed struct {
			Batches []videogen.Batch `json:"batches"`
		}
		if err := json.NewDecoder(response.Body).Decode(&listed); response.Code != http.StatusOK || err != nil {
			t.Fatalf("GET %s status=%d body=%s decode=%v", check.path, response.Code, response.Body.String(), err)
		}
		if len(listed.Batches) != 1 || listed.Batches[0].ID != check.want {
			t.Fatalf("GET %s batches=%#v, want only %s", check.path, listed.Batches, check.want)
		}
	}

	firstAsset, err := fixture.assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("first-image"), DisplayName: "first.png", MediaType: "image/png", Source: "contract"})
	if err != nil {
		t.Fatal(err)
	}
	secondAsset, err := fixture.assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("second-image"), DisplayName: "second.png", MediaType: "image/png", Source: "contract"})
	if err != nil {
		t.Fatal(err)
	}
	firstAsset, err = fixture.assets.SetState(firstAsset.ID, asset.StateActive)
	if err != nil {
		t.Fatal(err)
	}
	secondAsset, err = fixture.assets.SetState(secondAsset.ID, asset.StateActive)
	if err != nil {
		t.Fatal(err)
	}
	createItemsBody := []byte(`{"items":[` +
		`{"prompt":"referenced","negative_prompt":"none","enabled":true,"params_override":{},"init_image_id":"` + firstAsset.ID + `","end_image_id":"` + secondAsset.ID + `","control_frame_ids":[],"selected_assets":[]},` +
		`{"prompt":"executable","negative_prompt":"","enabled":true,"params_override":{},"control_frame_ids":[],"selected_assets":[]}]}`)
	createdResponse := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+primary.ID+"/items", createItemsBody)
	var created struct {
		Items []videogen.Item `json:"items"`
	}
	if err := json.NewDecoder(createdResponse.Body).Decode(&created); createdResponse.Code != http.StatusCreated || err != nil || len(created.Items) != 2 {
		t.Fatalf("create items status=%d body=%s decode=%v items=%d", createdResponse.Code, createdResponse.Body.String(), err, len(created.Items))
	}
	referencedID, executableID := created.Items[0].ID, created.Items[1].ID
	for _, strict := range []struct {
		name   string
		method string
		path   string
	}{
		{"item collection", http.MethodPost, "/api/v1/videos/batches/" + primary.ID + "/items"},
		{"item update", http.MethodPut, "/api/v1/videos/batches/" + primary.ID + "/items/" + referencedID},
		{"item move", http.MethodPost, "/api/v1/videos/batches/" + primary.ID + "/items/" + referencedID + "/move"},
		{"item execute", http.MethodPost, "/api/v1/videos/batches/" + primary.ID + "/items/" + executableID + "/execute"},
	} {
		response := fixture.request(strict.method, strict.path, []byte(`{"unknown":true}`))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s unknown status=%d body=%s", strict.name, response.Code, response.Body.String())
		}
	}

	detailResponse := fixture.request(http.MethodGet, "/api/v1/videos/batches/"+primary.ID, nil)
	rawDetail := detailResponse.Body.String()
	var detail videoBatchResponse
	if err := json.Unmarshal([]byte(rawDetail), &detail); detailResponse.Code != http.StatusOK || err != nil {
		t.Fatalf("detail status=%d body=%s decode=%v", detailResponse.Code, rawDetail, err)
	}
	if !detail.PresetAvailable || detail.Batch.ID != primary.ID || len(detail.Batch.Items) != 2 {
		t.Fatalf("detail batch=%#v preset_available=%v", detail.Batch, detail.PresetAvailable)
	}
	wantAssetIDs := []string{firstAsset.ID, secondAsset.ID}
	sort.Strings(wantAssetIDs)
	if len(detail.Assets) != 2 || detail.Assets[0].ID != wantAssetIDs[0] || detail.Assets[1].ID != wantAssetIDs[1] {
		t.Fatalf("asset summaries=%#v, want IDs=%v", detail.Assets, wantAssetIDs)
	}
	if detail.Assets[0].DisplayName == "" || detail.Assets[1].DisplayName == "" {
		t.Fatalf("asset summaries omitted display names: %#v", detail.Assets)
	}
	for _, forbidden := range []string{"stored_name", "b64_json", fixture.root} {
		if strings.Contains(rawDetail, forbidden) {
			t.Fatalf("detail leaked %q: %s", forbidden, rawDetail)
		}
	}

	updateItem := []byte(`{"prompt":"persisted prompt","negative_prompt":"updated","enabled":true,"params_override":{"strength":2},"init_image_id":"` + firstAsset.ID + `","end_image_id":"` + secondAsset.ID + `","control_frame_ids":[],"selected_assets":[]}`)
	updatedItemResponse := fixture.request(http.MethodPut, "/api/v1/videos/batches/"+primary.ID+"/items/"+referencedID, updateItem)
	var updatedItem videogen.Item
	if err := json.NewDecoder(updatedItemResponse.Body).Decode(&updatedItem); updatedItemResponse.Code != http.StatusOK || err != nil || updatedItem.ID != referencedID || updatedItem.Prompt != "persisted prompt" {
		t.Fatalf("update item status=%d body=%s decode=%v", updatedItemResponse.Code, updatedItemResponse.Body.String(), err)
	}
	persisted, ok := fixture.service.Get(primary.ID)
	if !ok || persisted.Items[0].Prompt != "persisted prompt" {
		t.Fatalf("prompt was not persisted: %#v", persisted.Items)
	}

	movedResponse := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+primary.ID+"/items/"+referencedID+"/move", []byte(`{"direction":1}`))
	var moved videogen.Batch
	if err := json.NewDecoder(movedResponse.Body).Decode(&moved); movedResponse.Code != http.StatusOK || err != nil {
		t.Fatalf("move status=%d body=%s decode=%v", movedResponse.Code, movedResponse.Body.String(), err)
	}
	if len(moved.Items) != 2 || moved.Items[0].ID != executableID || moved.Items[0].Order != 0 || moved.Items[1].ID != referencedID || moved.Items[1].Order != 1 {
		t.Fatalf("moved item order=%#v", moved.Items)
	}
	boundary := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+primary.ID+"/items/"+referencedID+"/move", []byte(`{"direction":1}`))
	if boundary.Code != http.StatusConflict {
		t.Fatalf("move boundary status=%d body=%s", boundary.Code, boundary.Body.String())
	}

	deletedItem := fixture.request(http.MethodDelete, "/api/v1/videos/batches/"+primary.ID+"/items/"+referencedID, nil)
	if deletedItem.Code != http.StatusNoContent {
		t.Fatalf("delete item status=%d body=%s", deletedItem.Code, deletedItem.Body.String())
	}
	afterItemDelete, ok := fixture.service.Get(primary.ID)
	if !ok || len(afterItemDelete.Items) != 1 || afterItemDelete.Items[0].ID != executableID || afterItemDelete.Items[0].Order != 0 {
		t.Fatalf("batch after item DELETE=%#v", afterItemDelete)
	}

	updatedBatchResponse := fixture.request(http.MethodPut, "/api/v1/videos/batches/"+primary.ID, []byte(`{"title":"renamed","folder":"updated","execution_kind":"http","preset_id":"sdcpp-video-local","concurrency":2,"common_params":{"seed":9},"timing":{"mode":"frames","video_frames":2,"fps":2}}`))
	var updatedBatch videogen.Batch
	if err := json.NewDecoder(updatedBatchResponse.Body).Decode(&updatedBatch); updatedBatchResponse.Code != http.StatusOK || err != nil {
		t.Fatalf("update batch status=%d body=%s decode=%v", updatedBatchResponse.Code, updatedBatchResponse.Body.String(), err)
	}
	if updatedBatch.Title != "renamed" || updatedBatch.Folder != "updated" || updatedBatch.Concurrency != 2 || updatedBatch.Timing.VideoFrames != 2 || updatedBatch.Timing.FPS != 2 || string(updatedBatch.CommonParams) != `{"seed":9}` {
		t.Fatalf("updated batch fields=%#v", updatedBatch)
	}

	strictExecute := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+primary.ID+"/execute", []byte(`{"unknown":true}`))
	if strictExecute.Code != http.StatusBadRequest {
		t.Fatalf("execute unknown status=%d body=%s", strictExecute.Code, strictExecute.Body.String())
	}
	execute := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+primary.ID+"/execute", []byte(`{}`))
	var accepted struct {
		Attempts []videogen.Attempt `json:"attempts"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); execute.Code != http.StatusAccepted || err != nil {
		t.Fatalf("execute status=%d body=%s decode=%v", execute.Code, execute.Body.String(), err)
	}
	if len(accepted.Attempts) != 1 || len(accepted.Attempts[0].ID) != 32 || accepted.Attempts[0].BatchID != primary.ID || accepted.Attempts[0].ItemID != executableID {
		t.Fatalf("execute attempts=%#v", accepted.Attempts)
	}
	cancel := fixture.request(http.MethodPost, "/api/v1/videos/attempts/"+accepted.Attempts[0].ID+"/cancel", []byte(`{}`))
	if cancel.Code != http.StatusAccepted {
		t.Fatalf("cancel status=%d body=%s", cancel.Code, cancel.Body.String())
	}

	for _, id := range []string{primary.ID, other.ID} {
		deleted := fixture.request(http.MethodDelete, "/api/v1/videos/batches/"+id, nil)
		if deleted.Code != http.StatusNoContent {
			t.Fatalf("delete batch %s status=%d body=%s", id, deleted.Code, deleted.Body.String())
		}
		missing := fixture.request(http.MethodGet, "/api/v1/videos/batches/"+id, nil)
		if missing.Code != http.StatusNotFound {
			t.Fatalf("GET deleted batch %s status=%d body=%s", id, missing.Code, missing.Body.String())
		}
	}

	invalidID := fixture.request(http.MethodGet, "/api/v1/videos/batches/not-an-id", nil)
	if invalidID.Code != http.StatusNotFound {
		t.Fatalf("invalid route status=%d body=%s", invalidID.Code, invalidID.Body.String())
	}
}

func TestVideoItemTimingOverrideRoundTripsThroughUnrelatedUpdate(t *testing.T) {
	fixture := newVideoAttemptFixture(t)
	defer fixture.manager.Shutdown(context.Background())
	batch, err := fixture.service.CreateBatch(videogen.CreateBatchInput{
		Title: "timing round trip", ExecutionKind: "http", PresetID: "sdcpp-video-local", Concurrency: 1,
		CommonParams: json.RawMessage(`{}`), Timing: videogen.TimingInput{Mode: "frames", VideoFrames: 49, FPS: 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	created := fixture.request(http.MethodPost, "/api/v1/videos/batches/"+batch.ID+"/items", []byte(`{"items":[{"prompt":"before","negative_prompt":"","enabled":true,"params_override":{},"timing_override":{"mode":"duration","duration_seconds":1.25,"fps":24},"control_frame_ids":[],"selected_assets":[]}]}`))
	var body struct {
		Items []videogen.Item `json:"items"`
	}
	if err := json.NewDecoder(created.Body).Decode(&body); created.Code != http.StatusCreated || err != nil || len(body.Items) != 1 {
		t.Fatalf("create status=%d body=%s decode=%v", created.Code, created.Body.String(), err)
	}
	item := body.Items[0]
	updated := fixture.request(http.MethodPut, "/api/v1/videos/batches/"+batch.ID+"/items/"+item.ID, []byte(`{"prompt":"after","negative_prompt":"","enabled":true,"params_override":{},"timing_override":{"mode":"duration","duration_seconds":1.25,"fps":24},"control_frame_ids":[],"selected_assets":[]}`))
	var got videogen.Item
	if err := json.NewDecoder(updated.Body).Decode(&got); updated.Code != http.StatusOK || err != nil {
		t.Fatalf("update status=%d body=%s decode=%v", updated.Code, updated.Body.String(), err)
	}
	if got.Prompt != "after" || got.TimingOverride == nil || got.TimingOverride.Mode != "duration" || got.TimingOverride.DurationSeconds != 1.25 || got.TimingOverride.FPS != 24 {
		t.Fatalf("unrelated edit lost duration timing override: %#v", got)
	}
}
