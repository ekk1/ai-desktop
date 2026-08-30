package imagegen

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

type serviceFixture struct {
	service    *Service
	assets     *asset.Repository
	batchRoot  string
	assetRoot  string
	assetIndex string
}

func TestServiceSynchronizesItemInputReferences(t *testing.T) {
	fixture := newServiceFixture(t)
	first := importImage(t, fixture.assets, "first.png")
	second := importImage(t, fixture.assets, "second.png")
	batch, err := fixture.service.CreateBatch(CreateBatchInput{Title: "Draw", ProviderID: "sdcpp-local", Concurrency: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(batch.BaseParams) != string(sdcpp.DefaultImageParams()) {
		t.Fatalf("default params = %s", batch.BaseParams)
	}
	items, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{
		Prompt: "p", InputAssets: InputAssets{InitImageID: first.ID, RefImageIDs: []string{first.ID, first.ID}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertReference(t, fixture.assets, first.ID, "image_item", items[0].ID, true)
	updated, err := fixture.service.UpdateItem(batch.ID, items[0].ID, UpdateItemInput{
		Prompt: "p", ParamsOverride: json.RawMessage(`{}`),
		InputAssets: InputAssets{InitImageID: second.ID, MaskImageID: second.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.InputAssets.InitImageID != second.ID {
		t.Fatalf("updated item = %#v", updated)
	}
	assertReference(t, fixture.assets, first.ID, "image_item", items[0].ID, false)
	assertReference(t, fixture.assets, second.ID, "image_item", items[0].ID, true)
}

func TestServiceRejectsUnknownOrNonImageInputWithoutMutation(t *testing.T) {
	fixture := newServiceFixture(t)
	nonImage, err := fixture.assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("memo"), DisplayName: "memo.txt", MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	beforeBatch, _ := fixture.service.Get(batch.ID)
	beforeAssets := fixture.assets.List(asset.Filter{})

	for name, assetID := range map[string]string{
		"unknown":   "99999999999999999999999999999999",
		"non-image": nonImage.ID,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InputAssets: InputAssets{InitImageID: assetID}}}); err == nil {
				t.Fatal("CreateItems succeeded")
			}
			gotBatch, _ := fixture.service.Get(batch.ID)
			if !reflect.DeepEqual(beforeBatch, gotBatch) {
				t.Fatalf("batch mutated: %#v", gotBatch)
			}
			if gotAssets := fixture.assets.List(asset.Filter{}); !reflect.DeepEqual(beforeAssets, gotAssets) {
				t.Fatalf("assets mutated: %#v", gotAssets)
			}
		})
	}
}

func TestServiceRetainsArchivedInputReference(t *testing.T) {
	fixture := newServiceFixture(t)
	input := importImage(t, fixture.assets, "input.png")
	if _, err := fixture.assets.SetState(input.ID, asset.StateActive); err != nil {
		t.Fatal(err)
	}
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	items, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InputAssets: InputAssets{InitImageID: input.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.assets.SetState(input.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.UpdateItem(batch.ID, items[0].ID, UpdateItemInput{
		Prompt: "edited", InputAssets: InputAssets{InitImageID: input.ID},
	}); err != nil {
		t.Fatal(err)
	}
	assertReference(t, fixture.assets, input.ID, "image_item", items[0].ID, true)
}

func TestServiceDeleteItemAndBatchReleaseAllReferences(t *testing.T) {
	fixture := newServiceFixture(t)
	first := importImage(t, fixture.assets, "first.png")
	second := importImage(t, fixture.assets, "second.png")
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	items, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{
		{Prompt: "one", InputAssets: InputAssets{InitImageID: first.ID}},
		{Prompt: "two", InputAssets: InputAssets{ControlImageID: second.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DeleteItem(batch.ID, items[0].ID); err != nil {
		t.Fatal(err)
	}
	assertReference(t, fixture.assets, first.ID, "image_item", items[0].ID, false)
	assertReference(t, fixture.assets, second.ID, "image_item", items[1].ID, true)
	if err := fixture.service.DeleteBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	assertReference(t, fixture.assets, second.ID, "image_item", items[1].ID, false)
}

func TestServiceRejectsDeletingActiveItemOrBatch(t *testing.T) {
	fixture := newServiceFixture(t)
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	items, _ := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "one"}})
	if _, err := fixture.service.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: validSnapshot()}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DeleteItem(batch.ID, items[0].ID); !errors.Is(err, ErrActiveAttempt) {
		t.Fatalf("delete active item error = %v", err)
	}
	if err := fixture.service.DeleteBatch(batch.ID); !errors.Is(err, ErrActiveAttempt) {
		t.Fatalf("delete active batch error = %v", err)
	}
}

func TestServiceRollsBackWhenReferenceSynchronizationFails(t *testing.T) {
	fixture := newServiceFixture(t)
	input := importImage(t, fixture.assets, "input.png")
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	batchPath := filepath.Join(fixture.batchRoot, batch.ID, "batch.json")
	beforeBatch, err := os.ReadFile(batchPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeAssets, err := os.ReadFile(fixture.assetIndex)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.assetRoot, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(fixture.assetRoot, 0o700) })

	if _, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InputAssets: InputAssets{InitImageID: input.ID}}}); err == nil {
		t.Fatal("CreateItems succeeded with unwritable asset store")
	}
	afterBatch, _ := os.ReadFile(batchPath)
	afterAssets, _ := os.ReadFile(fixture.assetIndex)
	if !bytes.Equal(beforeBatch, afterBatch) {
		t.Fatalf("batch document changed\nbefore=%s\nafter=%s", beforeBatch, afterBatch)
	}
	if !bytes.Equal(beforeAssets, afterAssets) {
		t.Fatalf("asset document changed\nbefore=%s\nafter=%s", beforeAssets, afterAssets)
	}
}

func newServiceFixture(t *testing.T) serviceFixture {
	t.Helper()
	root := t.TempDir()
	batchRoot := filepath.Join(root, "image-batches")
	batchRepository, err := OpenRepository(batchRoot)
	if err != nil {
		t.Fatal(err)
	}
	assetRoot := filepath.Join(root, "assets")
	assetIndex := filepath.Join(assetRoot, "index.json")
	assets, err := asset.OpenRepository(assetIndex, filepath.Join(assetRoot, "files"))
	if err != nil {
		t.Fatal(err)
	}
	return serviceFixture{
		service: NewService(batchRepository, assets), assets: assets,
		batchRoot: batchRoot, assetRoot: assetRoot, assetIndex: assetIndex,
	}
}

func importImage(t *testing.T, repository *asset.Repository, name string) asset.Asset {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 3))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	var contents bytes.Buffer
	if err := png.Encode(&contents, canvas); err != nil {
		t.Fatal(err)
	}
	created, err := repository.Import(asset.ImportInput{Reader: bytes.NewReader(contents.Bytes()), DisplayName: name, MediaType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func assertReference(t *testing.T, repository *asset.Repository, assetID, module, recordID string, want bool) {
	t.Helper()
	item, ok := repository.Get(assetID)
	if !ok {
		t.Fatalf("asset %q missing", assetID)
	}
	got := false
	for _, reference := range item.References {
		if reference == (asset.Reference{Module: module, RecordID: recordID}) {
			got = true
		}
	}
	if got != want {
		t.Fatalf("reference %s:%s present = %v, want %v; asset=%#v", module, recordID, got, want, item)
	}
}

func TestServiceErrorKinds(t *testing.T) {
	fixture := newServiceFixture(t)
	batch, _ := fixture.service.CreateBatch(validBatchInput())
	_, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{InputAssets: InputAssets{InitImageID: "99999999999999999999999999999999"}}})
	if !errors.Is(err, ErrImageAssetNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestServiceAttachesAttemptResultReference(t *testing.T) {
	fixture, batchID, itemID, attemptID := resultServiceFixture(t)
	result := importImage(t, fixture.assets, "result.png")
	got, err := fixture.service.AttachResult(batchID, itemID, attemptID, result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ResultAssetIDs) != 1 || got.ResultAssetIDs[0] != result.ID {
		t.Fatalf("attempt = %#v", got)
	}
	assertReference(t, fixture.assets, result.ID, "image_attempt", attemptID, true)
	got, err = fixture.service.AttachResult(batchID, itemID, attemptID, result.ID)
	if err != nil || len(got.ResultAssetIDs) != 1 {
		t.Fatalf("duplicate attach = %#v, %v", got, err)
	}
}

func TestServiceAttachesOnlyExistingImageResults(t *testing.T) {
	fixture, batchID, itemID, attemptID := resultServiceFixture(t)
	nonImage, err := fixture.assets.Import(asset.ImportInput{Reader: bytes.NewBufferString("memo"), DisplayName: "memo.txt", MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := fixture.service.Get(batchID)
	for _, id := range []string{"99999999999999999999999999999999", nonImage.ID} {
		if _, err := fixture.service.AttachResult(batchID, itemID, attemptID, id); err == nil {
			t.Fatalf("attached invalid result %q", id)
		}
	}
	after, _ := fixture.service.Get(batchID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("batch mutated: before=%#v after=%#v", before, after)
	}
}

func TestServiceRemovesResultReferenceWhenPersistenceFails(t *testing.T) {
	fixture, batchID, itemID, attemptID := resultServiceFixture(t)
	result := importImage(t, fixture.assets, "result.png")
	batchDirectory := filepath.Join(fixture.batchRoot, batchID)
	if err := os.Chmod(batchDirectory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(batchDirectory, 0o700) })
	if _, err := fixture.service.AttachResult(batchID, itemID, attemptID, result.ID); err == nil {
		t.Fatal("AttachResult succeeded with unwritable batch store")
	}
	assertReference(t, fixture.assets, result.ID, "image_attempt", attemptID, false)
	batch, _ := fixture.service.Get(batchID)
	if len(batch.Items[0].Attempts[0].ResultAssetIDs) != 0 {
		t.Fatalf("result persisted: %#v", batch.Items[0].Attempts[0])
	}
}

func TestServiceDeleteItemReleasesAttemptResultReferences(t *testing.T) {
	fixture, batchID, itemID, attemptID := resultServiceFixture(t)
	result := importImage(t, fixture.assets, "result.png")
	attached, err := fixture.service.AttachResult(batchID, itemID, attemptID, result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.UpdateAttempt(batchID, itemID, attemptID, UpdateAttemptInput{
		State: AttemptSucceeded, ResultAssetIDs: attached.ResultAssetIDs,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.DeleteItem(batchID, itemID); err != nil {
		t.Fatal(err)
	}
	assertReference(t, fixture.assets, result.ID, "image_attempt", attemptID, false)
}

func resultServiceFixture(t *testing.T) (serviceFixture, string, string, string) {
	t.Helper()
	fixture := newServiceFixture(t)
	batch, err := fixture.service.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "cat"}})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := fixture.service.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: validSnapshot()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.service.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptPolling}); err != nil {
		t.Fatal(err)
	}
	return fixture, batch.ID, items[0].ID, attempt.ID
}
