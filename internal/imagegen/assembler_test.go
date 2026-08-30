package imagegen

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

func TestAssemblerInjectsControlledImagesWithoutPersistingDataURLs(t *testing.T) {
	fixture := newAssemblerFixture(t)
	initImage := importColoredImage(t, fixture.assets, "init.png", color.RGBA{R: 255, A: 255})
	batch, item := createAssemblerItem(t, fixture, InputAssets{InitImageID: initImage.ID})
	prepared, snapshot, err := fixture.assembler.Build(batch, item, fixture.provider)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(prepared.Body, []byte(`"init_image":"data:image/png;base64,`)) {
		t.Fatalf("body = %s", prepared.Body)
	}
	if prepared.URL != fixture.provider.BaseURL+"/sdcpp/v1/img_gen" || prepared.Headers["Content-Type"] != "application/json" {
		t.Fatalf("prepared = %#v", prepared)
	}
	if prepared.ConnectTimeout != 10*time.Second || prepared.JobTimeout != time.Hour || prepared.PollInterval != 750*time.Millisecond {
		t.Fatalf("timeouts = %#v", prepared)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("base64")) || len(snapshot.InputAssets) != 1 || snapshot.InputAssets[0].SHA256 != initImage.SHA256 {
		t.Fatalf("snapshot = %s", encoded)
	}
	if string(snapshot.Params) != `{"height":512,"nested":{"keep":true,"replace":2},"width":768}` {
		t.Fatalf("snapshot params = %s", snapshot.Params)
	}
}

func TestAssemblerPreservesRefImageOrder(t *testing.T) {
	fixture := newAssemblerFixture(t)
	first := importColoredImage(t, fixture.assets, "first.png", color.RGBA{R: 255, A: 255})
	second := importColoredImage(t, fixture.assets, "second.png", color.RGBA{G: 255, A: 255})
	batch, item := createAssemblerItem(t, fixture, InputAssets{RefImageIDs: []string{second.ID, first.ID}})
	prepared, snapshot, err := fixture.assembler.Build(batch, item, fixture.provider)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		RefImages []string `json:"ref_images"`
	}
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.RefImages) != 2 || body.RefImages[0] != dataURL(t, fixture.assets, second.ID) || body.RefImages[1] != dataURL(t, fixture.assets, first.ID) {
		t.Fatalf("ref images = %#v", body.RefImages)
	}
	if len(snapshot.InputAssets) != 2 || snapshot.InputAssets[0].ID != second.ID || snapshot.InputAssets[1].ID != first.ID {
		t.Fatalf("snapshot assets = %#v", snapshot.InputAssets)
	}
}

func TestAssemblerAcceptsArchivedReferencedImage(t *testing.T) {
	fixture := newAssemblerFixture(t)
	input := importColoredImage(t, fixture.assets, "input.png", color.RGBA{B: 255, A: 255})
	if _, err := fixture.assets.SetState(input.ID, asset.StateActive); err != nil {
		t.Fatal(err)
	}
	batch, item := createAssemblerItem(t, fixture, InputAssets{InitImageID: input.ID})
	if _, err := fixture.assets.SetState(input.ID, asset.StateArchive); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.assembler.Build(batch, item, fixture.provider); err != nil {
		t.Fatal(err)
	}
}

func TestAssemblerRejectsMissingOrNonImageAsset(t *testing.T) {
	fixture := newAssemblerFixture(t)
	batch, item := createAssemblerItem(t, fixture, InputAssets{})
	item.InputAssets.InitImageID = "99999999999999999999999999999999"
	if _, _, err := fixture.assembler.Build(batch, item, fixture.provider); !errors.Is(err, ErrImageAssetNotFound) {
		t.Fatalf("missing error = %v", err)
	}
	nonImage, err := fixture.assets.Import(asset.ImportInput{Reader: strings.NewReader("memo"), DisplayName: "memo.txt", MediaType: "text/plain"})
	if err != nil {
		t.Fatal(err)
	}
	item.InputAssets.InitImageID = nonImage.ID
	if _, _, err := fixture.assembler.Build(batch, item, fixture.provider); !errors.Is(err, ErrImageAssetType) {
		t.Fatalf("type error = %v", err)
	}
}

func TestAssemblerEnforcesTotalInputLimit(t *testing.T) {
	fixture := newAssemblerFixture(t)
	first := importColoredImage(t, fixture.assets, "first.png", color.RGBA{R: 255, A: 255})
	second := importColoredImage(t, fixture.assets, "second.png", color.RGBA{G: 255, A: 255})
	batch, item := createAssemblerItem(t, fixture, InputAssets{InitImageID: first.ID, RefImageIDs: []string{second.ID}})
	fixture.provider.MaxImageBytes = first.Size + second.Size - 1
	if _, _, err := fixture.assembler.Build(batch, item, fixture.provider); !errors.Is(err, ErrImageInputLimit) {
		t.Fatalf("limit error = %v", err)
	}
}

func TestAssemblerRedactsSensitiveHeaders(t *testing.T) {
	fixture := newAssemblerFixture(t)
	fixture.provider.Headers = map[string]string{
		"Authorization": "Bearer secret", "proxy-authorization": "secret-2", "X-Api-Key": "secret-3",
		"api-KEY": "secret-4", "X-Trace": "visible",
	}
	batch, item := createAssemblerItem(t, fixture, InputAssets{})
	prepared, snapshot, err := fixture.assembler.Build(batch, item, fixture.provider)
	if err != nil {
		t.Fatal(err)
	}
	for key, secret := range fixture.provider.Headers {
		if prepared.Headers[key] != secret {
			t.Fatalf("prepared header %q = %q", key, prepared.Headers[key])
		}
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "Proxy-Authorization") || strings.EqualFold(key, "X-API-Key") || strings.EqualFold(key, "API-Key") {
			if snapshot.Provider.Headers[key] != "[REDACTED]" {
				t.Fatalf("snapshot header %q = %q", key, snapshot.Provider.Headers[key])
			}
		} else if snapshot.Provider.Headers[key] != secret {
			t.Fatalf("snapshot header %q = %q", key, snapshot.Provider.Headers[key])
		}
	}
	encoded, _ := json.Marshal(snapshot)
	if bytes.Contains(encoded, []byte("secret")) {
		t.Fatalf("snapshot leaked secret: %s", encoded)
	}
}

type assemblerFixture struct {
	assembler *Assembler
	service   *Service
	assets    *asset.Repository
	provider  sdcpp.ImageProvider
}

func newAssemblerFixture(t *testing.T) assemblerFixture {
	t.Helper()
	serviceFixture := newServiceFixture(t)
	return assemblerFixture{
		assembler: NewAssembler(serviceFixture.assets), service: serviceFixture.service,
		assets: serviceFixture.assets, provider: sdcpp.DefaultImageConfig().Providers[0],
	}
}

func createAssemblerItem(t *testing.T, fixture assemblerFixture, inputs InputAssets) (Batch, Item) {
	t.Helper()
	batch, err := fixture.service.CreateBatch(CreateBatchInput{
		Title: "Draw", ProviderID: fixture.provider.ID, Concurrency: 1,
		BaseParams: json.RawMessage(`{"width":768,"height":512,"nested":{"keep":true,"replace":1}}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := fixture.service.CreateItems(batch.ID, []CreateItemInput{{
		Prompt: "cat", NegativePrompt: "blur", ParamsOverride: json.RawMessage(`{"nested":{"replace":2}}`), InputAssets: inputs,
	}})
	if err != nil {
		t.Fatal(err)
	}
	batch, _ = fixture.service.Get(batch.ID)
	return batch, items[0]
}

func importColoredImage(t *testing.T, repository *asset.Repository, name string, pixel color.RGBA) asset.Asset {
	t.Helper()
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 3))
	canvas.Set(0, 0, pixel)
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

func dataURL(t *testing.T, repository *asset.Repository, id string) string {
	t.Helper()
	file, item, err := repository.OpenContent(id)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	contents, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	return "data:" + item.MediaType + ";base64," + base64.StdEncoding.EncodeToString(contents)
}
