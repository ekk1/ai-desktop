package videogen

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
)

// This fails if a video item's persisted inputs stop protecting every asset
// it references, including ordered CLI-style selections.
func TestServiceAddsReferencesForEveryOrderedVideoInput(t *testing.T) {
	service, assets, batch := videoServiceFixture(t)
	init := importFixtureAsset(t, assets, "image/png")
	control := importFixtureAsset(t, assets, "image/png")
	ref := importFixtureAsset(t, assets, "video/webm")
	items, err := service.CreateItems(batch.ID, []CreateItemInput{{
		Prompt: "p", Enabled: true, InitImageID: init.ID, ControlFrameIDs: []string{control.ID},
		SelectedAssets: []SelectedAsset{{AssetID: ref.ID, Role: "reference_video", Order: 0}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, init.ID, "video_item", items[0].ID, true)
	assertAssetReference(t, assets, control.ID, "video_item", items[0].ID, true)
	assertAssetReference(t, assets, ref.ID, "video_item", items[0].ID, true)
}

// This fails if archived assets can become new video inputs.
func TestServiceRejectsArchivedNewSelection(t *testing.T) {
	service, assets, batch := videoServiceFixture(t)
	archived := importFixtureAsset(t, assets, "image/png")
	if _, err := assets.SetState(archived.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InitImageID: archived.ID}}); !errors.Is(err, ErrVideoAssetNotActive) {
		t.Fatalf("CreateItems error = %v", err)
	}
}

// This fails if an edit drops a valid archived reference merely because the
// asset is no longer available for new selection.
func TestServiceAllowsAlreadyReferencedArchivedInput(t *testing.T) {
	service, assets, batch := videoServiceFixture(t)
	input := importFixtureAsset(t, assets, "image/png")
	items, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InitImageID: input.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.SetState(input.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateItem(batch.ID, items[0].ID, UpdateItemInput{Prompt: "edited", InitImageID: input.ID}); err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, input.ID, "video_item", items[0].ID, true)
}

// This fails if the image-only HTTP frame roles accept arbitrary media.
func TestServiceRejectsNonImageHTTPFrames(t *testing.T) {
	service, assets, batch := videoServiceFixture(t)
	nonImage := importFixtureAsset(t, assets, "video/webm")
	if _, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", ControlFrameIDs: []string{nonImage.ID}}}); !errors.Is(err, ErrVideoAssetType) {
		t.Fatalf("CreateItems error = %v", err)
	}
}

// This fails if a reference write failure leaves the video repository mutated.
func TestServiceRollsBackReferenceFailure(t *testing.T) {
	fixture := newVideoServiceFixture(t)
	input := importFixtureAsset(t, fixture.assets, "image/png")
	batch, err := fixture.service.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	before, ok := fixture.service.Get(batch.ID)
	if !ok {
		t.Fatal("batch missing")
	}
	if err := os.Rename(fixture.assetIndex, fixture.assetIndex+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(fixture.assetIndex, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(fixture.assetIndex)
		_ = os.Rename(fixture.assetIndex+".saved", fixture.assetIndex)
	})

	if _, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InitImageID: input.ID}}); err == nil {
		t.Fatal("CreateItems succeeded with an unwritable asset index")
	}
	after, _ := fixture.service.Get(batch.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("batch mutated: before=%#v after=%#v", before, after)
	}
}

// This fails if archived generated video results cannot remain attached.
func TestServiceAttachesArchivedVideoResult(t *testing.T) {
	service, assets, batchID, itemID, attemptID := videoResultFixture(t)
	result := importFixtureAsset(t, assets, "video/webm")
	if _, err := assets.SetState(result.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	got, err := service.AttachVideoResult(batchID, itemID, attemptID, result.ID)
	if err != nil || got.OutputAssetID != result.ID {
		t.Fatalf("AttachVideoResult = %#v, %v", got, err)
	}
	assertAssetReference(t, assets, result.ID, "video_result", attemptID, true)
}

// This fails if deleting a completed item or batch leaves input, attempt
// snapshot, or result references behind.
func TestServiceDeleteItemAndBatchReleaseAllVideoReferences(t *testing.T) {
	service, assets, batch := videoServiceFixture(t)
	itemInput := importFixtureAsset(t, assets, "image/png")
	attemptInput := importFixtureAsset(t, assets, "image/png")
	result := importFixtureAsset(t, assets, "video/webm")
	items, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p", InitImageID: itemInput.ID}})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshotFor(attemptInput)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AttachVideoResult(batch.ID, items[0].ID, attempt.ID, result.ID); err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, itemInput.ID, "video_item", items[0].ID, true)
	assertAssetReference(t, assets, attemptInput.ID, "video_attempt", attempt.ID, true)
	assertAssetReference(t, assets, result.ID, "video_result", attempt.ID, true)
	if _, err := service.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptCancelled, OutputAssetID: result.ID}); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteItem(batch.ID, items[0].ID); err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, itemInput.ID, "video_item", items[0].ID, false)
	assertAssetReference(t, assets, attemptInput.ID, "video_attempt", attempt.ID, false)
	assertAssetReference(t, assets, result.ID, "video_result", attempt.ID, false)

	remainingInput := importFixtureAsset(t, assets, "image/png")
	remaining, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "remaining", InitImageID: remainingInput.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteBatch(batch.ID); err != nil {
		t.Fatal(err)
	}
	assertAssetReference(t, assets, remainingInput.ID, "video_item", remaining[0].ID, false)
}

type videoServiceFixtureData struct {
	service    *Service
	assets     *asset.Repository
	assetIndex string
}

func newVideoServiceFixture(t *testing.T) videoServiceFixtureData {
	t.Helper()
	root := t.TempDir()
	repository, err := OpenRepository(filepath.Join(root, "video-batches"))
	if err != nil {
		t.Fatal(err)
	}
	assetRoot := filepath.Join(root, "assets")
	assetIndex := filepath.Join(assetRoot, "index.json")
	assets, err := asset.OpenRepository(assetIndex, filepath.Join(assetRoot, "files"))
	if err != nil {
		t.Fatal(err)
	}
	return videoServiceFixtureData{service: NewService(repository, assets), assets: assets, assetIndex: assetIndex}
}

func videoServiceFixture(t *testing.T) (*Service, *asset.Repository, Batch) {
	t.Helper()
	fixture := newVideoServiceFixture(t)
	batch, err := fixture.service.CreateBatch(validBatchInput())
	if err != nil {
		t.Fatal(err)
	}
	return fixture.service, fixture.assets, batch
}

func videoResultFixture(t *testing.T) (*Service, *asset.Repository, string, string, string) {
	t.Helper()
	service, assets, batch := videoServiceFixture(t)
	items, err := service.CreateItems(batch.ID, []CreateItemInput{{Prompt: "p"}})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := service.CreateAttempt(batch.ID, items[0].ID, CreateAttemptInput{State: AttemptQueued, Snapshot: snapshotFor()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateAttempt(batch.ID, items[0].ID, attempt.ID, UpdateAttemptInput{State: AttemptSubmitting}); err != nil {
		t.Fatal(err)
	}
	return service, assets, batch.ID, items[0].ID, attempt.ID
}

func importFixtureAsset(t *testing.T, assets *asset.Repository, mediaType string) asset.Asset {
	t.Helper()
	contents := []byte("video fixture")
	if mediaType == "image/png" {
		canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
		canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, canvas); err != nil {
			t.Fatal(err)
		}
		contents = encoded.Bytes()
	}
	created, err := assets.Import(asset.ImportInput{Reader: bytes.NewReader(contents), DisplayName: "fixture", MediaType: mediaType})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.SetState(created.ID, asset.StateActive); err != nil {
		t.Fatal(err)
	}
	return created
}

func snapshotFor(items ...asset.Asset) Snapshot {
	snapshot := validVideoSnapshot()
	snapshot.InputAssets = make([]AssetSnapshot, 0, len(items))
	for index, item := range items {
		snapshot.InputAssets = append(snapshot.InputAssets, AssetSnapshot{ID: item.ID, SHA256: item.SHA256, MediaType: item.MediaType, DisplayName: item.DisplayName, Role: "input", Order: index, Size: item.Size})
	}
	return snapshot
}

func assertAssetReference(t *testing.T, assets *asset.Repository, assetID, module, recordID string, want bool) {
	t.Helper()
	item, ok := assets.Get(assetID)
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
