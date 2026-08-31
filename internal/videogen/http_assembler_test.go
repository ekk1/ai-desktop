package videogen

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

func TestHTTPAssemblerInjectsFramesInOrderWithoutSnapshotBase64(t *testing.T) {
	assembler, assets, batch, item, provider := httpAssemblerFixture(t)
	first := importColoredPNG(t, assets, color.RGBA{R: 255, A: 255})
	second := importColoredPNG(t, assets, color.RGBA{G: 255, A: 255})
	item.ControlFrameIDs = []string{first.ID, second.ID}
	prepared, snapshot, err := assembler.BuildHTTP(batch, item, provider)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.URL != provider.BaseURL+"/sdcpp/v1/vid_gen" || !bytes.Contains(prepared.Body, []byte(`"control_frames":["data:image/png;base64,`)) {
		t.Fatalf("prepared body = %s", prepared.Body)
	}
	var body struct {
		ControlFrames []string `json:"control_frames"`
	}
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.ControlFrames) != 2 || body.ControlFrames[0] == body.ControlFrames[1] {
		t.Fatalf("control frames = %#v", body.ControlFrames)
	}
	encoded, _ := json.Marshal(snapshot)
	if bytes.Contains(encoded, []byte("base64")) || len(snapshot.InputAssets) != 2 || snapshot.InputAssets[0].ID != first.ID || snapshot.InputAssets[1].ID != second.ID {
		t.Fatalf("snapshot = %s", encoded)
	}
}

func TestHTTPAssemblerUsesItemTimingAndManagedMedia(t *testing.T) {
	assembler, assets, batch, item, provider := httpAssemblerFixture(t)
	init := importPNG(t, assets)
	end := importPNG(t, assets)
	item.InitImageID, item.EndImageID = init.ID, end.ID
	item.TimingOverride = &TimingInput{Mode: "frames", VideoFrames: 37, FPS: 23}
	prepared, snapshot, err := assembler.BuildHTTP(batch, item, provider)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["fps"].(float64) != 23 || body["video_frames"].(float64) != 37 || !strings.HasPrefix(body["init_image"].(string), "data:image/png;base64,") || !strings.HasPrefix(body["end_image"].(string), "data:image/png;base64,") {
		t.Fatalf("body = %#v", body)
	}
	if snapshot.Timing.RequestedFrames != 37 || snapshot.Timing.FPS != 23 || snapshot.InputAssets[0].Role != "init" || snapshot.InputAssets[1].Role != "end" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestHTTPAssemblerRejectsManagedParametersThatCouldReplaceMedia(t *testing.T) {
	assembler, assets, batch, item, provider := httpAssemblerFixture(t)
	item.InitImageID = importPNG(t, assets).ID
	batch.CommonParams = json.RawMessage(`{"init_image":"stolen"}`)
	if _, _, err := assembler.BuildHTTP(batch, item, provider); err == nil {
		t.Fatal("BuildHTTP accepted a managed init_image parameter")
	}
}

func TestHTTPAssemblerRejectsInactiveUnretainedAndNonImageInputs(t *testing.T) {
	assembler, assets, batch, item, provider := httpAssemblerFixture(t)
	archived := importPNG(t, assets)
	if _, err := assets.SetState(archived.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	item.InitImageID = archived.ID
	if _, _, err := assembler.BuildHTTP(batch, item, provider); !errors.Is(err, ErrVideoAssetNotActive) {
		t.Fatalf("unretained archived error = %v", err)
	}
	if _, err := assets.AddReference(archived.ID, asset.Reference{Module: videoItemReferenceModule, RecordID: item.ID}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := assembler.BuildHTTP(batch, item, provider); err != nil {
		t.Fatalf("retained archived image = %v", err)
	}
	nonImage, err := assets.Import(asset.ImportInput{Reader: strings.NewReader("memo"), DisplayName: "memo.txt", MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.SetState(nonImage.ID, asset.StateActive); err != nil {
		t.Fatal(err)
	}
	item.InitImageID = nonImage.ID
	if _, _, err := assembler.BuildHTTP(batch, item, provider); !errors.Is(err, ErrVideoAssetType) {
		t.Fatalf("non-image error = %v", err)
	}
}

func TestHTTPAssemblerEnforcesInputAndRequestLimits(t *testing.T) {
	assembler, assets, batch, item, provider := httpAssemblerFixture(t)
	first := importPNG(t, assets)
	second := importPNG(t, assets)
	item.ControlFrameIDs = []string{first.ID, second.ID}
	provider.MaxInputImageBytes = first.Size + second.Size - 1
	if _, _, err := assembler.BuildHTTP(batch, item, provider); !errors.Is(err, ErrVideoInputLimit) {
		t.Fatalf("input limit error = %v", err)
	}
	provider.MaxInputImageBytes = first.Size + second.Size
	provider.MaxRequestBytes = 1
	if _, _, err := assembler.BuildHTTP(batch, item, provider); !errors.Is(err, ErrVideoRequestLimit) {
		t.Fatalf("request limit error = %v", err)
	}
}

func TestHTTPAssemblerRedactsProviderSecretsInSnapshot(t *testing.T) {
	assembler, _, batch, item, provider := httpAssemblerFixture(t)
	provider.Headers = map[string]string{"Authorization": "Bearer secret", "X-API-Key": "secret-2", "X-Trace": "visible"}
	prepared, snapshot, err := assembler.BuildHTTP(batch, item, provider)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Headers["Authorization"] != "Bearer secret" || snapshot.HTTPProvider.Headers["Authorization"] != "[REDACTED]" || snapshot.HTTPProvider.Headers["X-API-Key"] != "[REDACTED]" || snapshot.HTTPProvider.Headers["X-Trace"] != "visible" {
		t.Fatalf("prepared = %#v, snapshot = %#v", prepared, snapshot)
	}
	encoded, _ := json.Marshal(snapshot)
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("snapshot leaked secret: %s", encoded)
	}
}

func httpAssemblerFixture(t *testing.T) (*HTTPAssembler, *asset.Repository, Batch, Item, videoconfig.HTTPProvider) {
	t.Helper()
	assets, err := asset.OpenRepository(t.TempDir()+"/assets.json", t.TempDir()+"/files")
	if err != nil {
		t.Fatal(err)
	}
	provider := videoconfig.Default().HTTPProviders[0]
	return NewHTTPAssembler(assets), assets,
		Batch{ID: "batch", CommonParams: json.RawMessage(`{"sample_params":{"eta":0.5}}`), Timing: TimingInput{Mode: "duration", DurationSeconds: 2.01, FPS: 16}},
		Item{ID: "item", Prompt: "cat", NegativePrompt: "blur", ParamsOverride: json.RawMessage(`{"seed":7}`)}, provider
}

func importPNG(t *testing.T, assets *asset.Repository) asset.Asset {
	return importColoredPNG(t, assets, color.RGBA{R: 255, A: 255})
}

func importColoredPNG(t *testing.T, assets *asset.Repository, pixel color.RGBA) asset.Asset {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, pixel)
	var contents bytes.Buffer
	if err := png.Encode(&contents, canvas); err != nil {
		t.Fatal(err)
	}
	item, err := assets.Import(asset.ImportInput{Reader: bytes.NewReader(contents.Bytes()), DisplayName: "input.png", MediaType: "image/png"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assets.SetState(item.ID, asset.StateActive); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestHTTPAssemblerExposesProviderTimeouts(t *testing.T) {
	assembler, _, batch, item, provider := httpAssemblerFixture(t)
	prepared, _, err := assembler.BuildHTTP(batch, item, provider)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ConnectTimeout != 10*time.Second || prepared.JobTimeout != 24*time.Hour || prepared.PollInterval != 750*time.Millisecond {
		t.Fatalf("timeouts = %#v", prepared)
	}
}
