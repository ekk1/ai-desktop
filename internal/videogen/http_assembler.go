package videogen

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

var (
	ErrVideoInputLimit   = errors.New("video input exceeds provider byte limit")
	ErrVideoRequestLimit = errors.New("video request exceeds provider byte limit")
)

// PreparedHTTP is the complete, bounded request ready for an HTTP executor.
type PreparedHTTP struct {
	URL            string
	Headers        map[string]string
	Body           []byte
	Provider       videoconfig.HTTPProvider
	ConnectTimeout time.Duration
	JobTimeout     time.Duration
	PollInterval   time.Duration
}

// HTTPAssembler loads retained input assets and produces native video requests.
type HTTPAssembler struct {
	assets *asset.Repository
}

func NewHTTPAssembler(assets *asset.Repository) *HTTPAssembler {
	return &HTTPAssembler{assets: assets}
}

func (assembler *HTTPAssembler) BuildHTTP(batch Batch, item Item, provider videoconfig.HTTPProvider) (PreparedHTTP, Snapshot, error) {
	if assembler == nil || assembler.assets == nil {
		return PreparedHTTP{}, Snapshot{}, fmt.Errorf("video asset repository is required")
	}
	if err := (videoconfig.Config{HTTPProviders: []videoconfig.HTTPProvider{provider}}).Validate(); err != nil {
		return PreparedHTTP{}, Snapshot{}, fmt.Errorf("HTTP provider: %w", err)
	}
	params, err := MergeParams(provider.DefaultParams, batch.CommonParams, item.ParamsOverride)
	if err != nil {
		return PreparedHTTP{}, Snapshot{}, err
	}
	timing, err := ResolveTiming(batch.Timing, item.TimingOverride)
	if err != nil {
		return PreparedHTTP{}, Snapshot{}, err
	}

	request := cloneParamsObject(params)
	request["prompt"] = item.Prompt
	request["negative_prompt"] = item.NegativePrompt
	request["fps"] = timing.FPS
	request["video_frames"] = timing.RequestedFrames

	remaining := provider.MaxInputImageBytes
	snapshots := make([]AssetSnapshot, 0, 2+len(item.ControlFrameIDs))
	if item.InitImageID != "" {
		loaded, dataURL, consumed, err := assembler.loadHTTPImage(item.ID, item.InitImageID, remaining)
		if err != nil {
			return PreparedHTTP{}, Snapshot{}, err
		}
		remaining -= consumed
		request["init_image"] = dataURL
		snapshots = append(snapshots, snapshotAsset(loaded, "init", len(snapshots)))
	}
	if item.EndImageID != "" {
		loaded, dataURL, consumed, err := assembler.loadHTTPImage(item.ID, item.EndImageID, remaining)
		if err != nil {
			return PreparedHTTP{}, Snapshot{}, err
		}
		remaining -= consumed
		request["end_image"] = dataURL
		snapshots = append(snapshots, snapshotAsset(loaded, "end", len(snapshots)))
	}
	if len(item.ControlFrameIDs) > 0 {
		frames := make([]string, 0, len(item.ControlFrameIDs))
		for _, id := range item.ControlFrameIDs {
			loaded, dataURL, consumed, err := assembler.loadHTTPImage(item.ID, id, remaining)
			if err != nil {
				return PreparedHTTP{}, Snapshot{}, err
			}
			remaining -= consumed
			frames = append(frames, dataURL)
			snapshots = append(snapshots, snapshotAsset(loaded, "control", len(snapshots)))
		}
		request["control_frames"] = frames
	}

	body, err := json.Marshal(request)
	if err != nil {
		return PreparedHTTP{}, Snapshot{}, fmt.Errorf("encode video request: %w", err)
	}
	if int64(len(body)) > provider.MaxRequestBytes {
		return PreparedHTTP{}, Snapshot{}, ErrVideoRequestLimit
	}
	canonicalParams, err := json.Marshal(params)
	if err != nil {
		return PreparedHTTP{}, Snapshot{}, fmt.Errorf("encode merged video params: %w", err)
	}
	headers := cloneHTTPHeaders(provider.Headers)
	if !hasHTTPHeader(headers, "Content-Type") {
		headers["Content-Type"] = "application/json"
	}
	preparedProvider := provider
	preparedProvider.Headers = cloneHTTPHeaders(provider.Headers)
	redactedProvider := provider
	redactedProvider.Headers = redactHTTPHeaders(provider.Headers)
	return PreparedHTTP{
			URL: provider.BaseURL + "/sdcpp/v1/vid_gen", Headers: headers, Body: body, Provider: preparedProvider,
			ConnectTimeout: time.Duration(provider.ConnectTimeoutSeconds) * time.Second,
			JobTimeout:     time.Duration(provider.JobTimeoutSeconds) * time.Second,
			PollInterval:   time.Duration(provider.PollIntervalMilliseconds) * time.Millisecond,
		}, Snapshot{
			ExecutionKind: videoconfig.ExecutionHTTP, HTTPProvider: &redactedProvider, Params: canonicalParams,
			Prompt: item.Prompt, NegativePrompt: item.NegativePrompt, Timing: timing, InputAssets: snapshots,
			CreatedAt: time.Now().UTC(),
		}, nil
}

func (assembler *HTTPAssembler) loadHTTPImage(itemID, assetID string, remaining int64) (asset.Asset, string, int64, error) {
	item, ok := assembler.assets.Get(assetID)
	if !ok {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrVideoAssetNotFound, assetID)
	}
	if !strings.HasPrefix(strings.ToLower(item.MediaType), "image/") {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrVideoAssetType, assetID)
	}
	if item.State != asset.StateActive && !containsReference(item, asset.Reference{Module: videoItemReferenceModule, RecordID: itemID}) {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrVideoAssetNotActive, assetID)
	}
	if remaining < 0 || item.Size > remaining {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrVideoInputLimit, assetID)
	}
	file, current, err := assembler.assets.OpenContent(assetID)
	if err != nil {
		return asset.Asset{}, "", 0, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, remaining+1))
	if err != nil {
		return asset.Asset{}, "", 0, fmt.Errorf("read video input %q: %w", assetID, err)
	}
	if int64(len(contents)) > remaining {
		return asset.Asset{}, "", 0, fmt.Errorf("%w: %s", ErrVideoInputLimit, assetID)
	}
	return current, "data:" + current.MediaType + ";base64," + base64.StdEncoding.EncodeToString(contents), int64(len(contents)), nil
}

func snapshotAsset(item asset.Asset, role string, order int) AssetSnapshot {
	return AssetSnapshot{ID: item.ID, SHA256: item.SHA256, MediaType: item.MediaType, DisplayName: item.DisplayName, Size: item.Size, Role: role, Order: order}
}

func cloneParamsObject(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = cloneParamsValue(value)
	}
	return clone
}

func cloneHTTPHeaders(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func redactHTTPHeaders(source map[string]string) map[string]string {
	clone := cloneHTTPHeaders(source)
	for key := range clone {
		if sensitiveHTTPHeader(key) {
			clone[key] = "[REDACTED]"
		}
	}
	return clone
}

func sensitiveHTTPHeader(name string) bool {
	return strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization") ||
		strings.EqualFold(name, "X-API-Key") || strings.EqualFold(name, "API-Key")
}

func hasHTTPHeader(headers map[string]string, name string) bool {
	for key := range headers {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}
