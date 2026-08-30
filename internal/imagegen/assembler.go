package imagegen

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

var (
	ErrImageInputLimit           = errors.New("image input exceeds provider byte limit")
	ErrArchivedInputUnreferenced = errors.New("archived image input is not referenced by the item")
)

type PreparedRequest struct {
	URL              string
	Headers          map[string]string
	Body             []byte
	ConnectTimeout   time.Duration
	JobTimeout       time.Duration
	PollInterval     time.Duration
	MaxResponseBytes int64
	MaxImageBytes    int64
}

type Assembler struct {
	assets *asset.Repository
}

type imageInput struct {
	id  string
	set func(*sdcpp.ImageFields, string)
}

func NewAssembler(assets *asset.Repository) *Assembler {
	return &Assembler{assets: assets}
}

func (assembler *Assembler) Build(batch Batch, item Item, provider sdcpp.ImageProvider) (PreparedRequest, Snapshot, error) {
	if err := (sdcpp.ImageConfig{Providers: []sdcpp.ImageProvider{provider}}).Validate(); err != nil {
		return PreparedRequest{}, Snapshot{}, fmt.Errorf("image provider: %w", err)
	}
	params, err := sdcpp.MergeImageParams(batch.BaseParams, item.ParamsOverride)
	if err != nil {
		return PreparedRequest{}, Snapshot{}, err
	}
	canonicalParams, err := json.Marshal(params)
	if err != nil {
		return PreparedRequest{}, Snapshot{}, fmt.Errorf("encode merged image params: %w", err)
	}

	fields := sdcpp.ImageFields{}
	snapshots := make([]AssetSnapshot, 0)
	snapshotted := make(map[string]struct{})
	remaining := provider.MaxImageBytes
	inputs := orderedImageInputs(item.InputAssets)
	for _, input := range inputs {
		loaded, dataURL, consumed, err := assembler.loadInput(item.ID, input.id, remaining)
		if err != nil {
			return PreparedRequest{}, Snapshot{}, err
		}
		remaining -= consumed
		input.set(&fields, dataURL)
		if _, exists := snapshotted[loaded.ID]; !exists {
			snapshotted[loaded.ID] = struct{}{}
			snapshots = append(snapshots, AssetSnapshot{
				ID: loaded.ID, SHA256: loaded.SHA256, MediaType: loaded.MediaType,
				DisplayName: loaded.DisplayName, Size: loaded.Size, Width: loaded.Width, Height: loaded.Height,
			})
		}
	}
	body, err := sdcpp.RenderImageRequest(params, item.Prompt, item.NegativePrompt, fields)
	if err != nil {
		return PreparedRequest{}, Snapshot{}, err
	}
	headers := cloneProviderHeaders(provider.Headers)
	if !hasHeader(headers, "Content-Type") {
		headers["Content-Type"] = "application/json"
	}
	prepared := PreparedRequest{
		URL: provider.BaseURL + "/sdcpp/v1/img_gen", Headers: headers, Body: body,
		ConnectTimeout:   time.Duration(provider.ConnectTimeoutSeconds) * time.Second,
		JobTimeout:       time.Duration(provider.JobTimeoutSeconds) * time.Second,
		PollInterval:     time.Duration(provider.PollIntervalMilliseconds) * time.Millisecond,
		MaxResponseBytes: provider.MaxResponseBytes, MaxImageBytes: provider.MaxImageBytes,
	}
	snapshotProvider := provider
	snapshotProvider.Headers = redactProviderHeaders(provider.Headers)
	snapshot := Snapshot{
		Provider: snapshotProvider, Params: canonicalParams, Prompt: item.Prompt,
		NegativePrompt: item.NegativePrompt, InputAssets: snapshots, CreatedAt: time.Now().UTC(),
	}
	return prepared, snapshot, nil
}

func (assembler *Assembler) loadInput(itemID, assetID string, remaining int64) (asset.Asset, string, int64, error) {
	item, ok := assembler.assets.Get(assetID)
	if !ok {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrImageAssetNotFound, assetID)
	}
	if !strings.HasPrefix(strings.ToLower(item.MediaType), "image/") {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrImageAssetType, assetID)
	}
	if item.State == asset.StateArchive && !containsAssetReference(item, asset.Reference{Module: imageItemReferenceModule, RecordID: itemID}) {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrArchivedInputUnreferenced, assetID)
	}
	if remaining < 0 || item.Size > remaining {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrImageInputLimit, assetID)
	}
	file, current, err := assembler.assets.OpenContent(assetID)
	if err != nil {
		return asset.Asset{}, "", 0, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, remaining+1))
	if err != nil {
		return asset.Asset{}, "", 0, fmt.Errorf("read image input %q: %w", assetID, err)
	}
	if int64(len(contents)) > remaining {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrImageInputLimit, assetID)
	}
	dataURL := "data:" + current.MediaType + ";base64," + base64.StdEncoding.EncodeToString(contents)
	return current, dataURL, int64(len(contents)), nil
}

func orderedImageInputs(input InputAssets) []imageInput {
	result := make([]imageInput, 0, 4+len(input.RefImageIDs))
	if input.InitImageID != "" {
		result = append(result, imageInput{id: input.InitImageID, set: func(fields *sdcpp.ImageFields, value string) { fields.InitImage = value }})
	}
	for _, id := range input.RefImageIDs {
		result = append(result, imageInput{id: id, set: func(fields *sdcpp.ImageFields, value string) {
			fields.RefImages = append(fields.RefImages, value)
		}})
	}
	if input.MaskImageID != "" {
		result = append(result, imageInput{id: input.MaskImageID, set: func(fields *sdcpp.ImageFields, value string) { fields.MaskImage = value }})
	}
	if input.ControlImageID != "" {
		result = append(result, imageInput{id: input.ControlImageID, set: func(fields *sdcpp.ImageFields, value string) { fields.ControlImage = value }})
	}
	if input.IPAdapterImageID != "" {
		result = append(result, imageInput{id: input.IPAdapterImageID, set: func(fields *sdcpp.ImageFields, value string) { fields.IPAdapterImage = value }})
	}
	return result
}

func cloneProviderHeaders(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func redactProviderHeaders(source map[string]string) map[string]string {
	clone := cloneProviderHeaders(source)
	for key := range clone {
		if sensitiveProviderHeader(key) {
			clone[key] = "[REDACTED]"
		}
	}
	return clone
}

func sensitiveProviderHeader(name string) bool {
	return strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") ||
		strings.EqualFold(name, "X-API-Key") || strings.EqualFold(name, "API-Key")
}

func hasHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
