package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/videogen"
)

type videoBatchHandler struct {
	service *videogen.Service
	assets  *asset.Repository
	config  *config.Repository
	manager *videogen.Manager
	maxBody int64
}
type videoBatchResponse struct {
	Batch           videogen.Batch      `json:"batch"`
	PresetAvailable bool                `json:"preset_available"`
	Assets          []videoAssetSummary `json:"assets"`
}
type videoAssetSummary struct {
	ID          string      `json:"id"`
	SHA256      string      `json:"sha256"`
	MediaType   string      `json:"media_type"`
	DisplayName string      `json:"display_name"`
	Size        int64       `json:"size"`
	State       asset.State `json:"state"`
}

func validVideoID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (h videoBatchHandler) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/videos/batches"), "/")
	if p == "" {
		h.collection(w, r)
		return
	}
	s := strings.Split(p, "/")
	if !validVideoID(s[0]) {
		writeAPIError(w, 404, "not_found", "resource not found")
		return
	}
	id := s[0]
	switch {
	case len(s) == 1:
		h.batch(w, r, id)
	case len(s) == 2 && s[1] == "items":
		h.createItems(w, r, id)
	case len(s) == 2 && s[1] == "execute" && h.manager != nil:
		(videoAttemptHandler{manager: h.manager, maxBody: h.maxBody}).executeBatch(w, r, id)
	case len(s) == 2 && s[1] == "events" && h.manager != nil:
		(videoAttemptHandler{manager: h.manager, maxBody: h.maxBody}).events(w, r, id)
	case len(s) == 3 && s[1] == "items" && validVideoID(s[2]):
		h.item(w, r, id, s[2])
	case len(s) == 4 && s[1] == "items" && validVideoID(s[2]) && s[3] == "move":
		h.moveItem(w, r, id, s[2])
	case len(s) == 4 && s[1] == "items" && validVideoID(s[2]) && s[3] == "execute" && h.manager != nil:
		(videoAttemptHandler{manager: h.manager, maxBody: h.maxBody}).executeItem(w, r, id, s[2])
	default:
		writeAPIError(w, 404, "not_found", "resource not found")
	}
}
func (h videoBatchHandler) collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, struct {
			Batches []videogen.Batch `json:"batches"`
		}{h.service.List(videogen.Filter{Query: r.URL.Query().Get("q"), Folder: r.URL.Query().Get("folder"), PresetID: r.URL.Query().Get("preset_id")})})
	case http.MethodPost:
		var in videogen.CreateBatchInput
		if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
			return
		}
		out, err := h.service.CreateBatch(in)
		if err != nil {
			writeVideoAPIError(w, err)
			return
		}
		writeJSON(w, 201, out)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}
func (h videoBatchHandler) batch(w http.ResponseWriter, r *http.Request, id string) {
	switch r.Method {
	case http.MethodGet:
		b, ok := h.service.Get(id)
		if !ok {
			writeVideoAPIError(w, videogen.ErrBatchNotFound)
			return
		}
		writeJSON(w, 200, h.detail(b))
	case http.MethodPut:
		var in videogen.UpdateBatchInput
		if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
			return
		}
		b, err := h.service.UpdateBatch(id, in)
		if err != nil {
			writeVideoAPIError(w, err)
			return
		}
		writeJSON(w, 200, b)
	case http.MethodDelete:
		if err := h.service.DeleteBatch(id); err != nil {
			writeVideoAPIError(w, err)
			return
		}
		w.WriteHeader(204)
	default:
		methodNotAllowed(w, "GET, PUT, DELETE")
	}
}
func (h videoBatchHandler) createItems(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct {
		Items []videogen.CreateItemInput `json:"items"`
	}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	items, err := h.service.CreateItems(id, in.Items)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	writeJSON(w, 201, struct {
		Items []videogen.Item `json:"items"`
	}{items})
}
func (h videoBatchHandler) item(w http.ResponseWriter, r *http.Request, batch, item string) {
	switch r.Method {
	case http.MethodPut:
		var in videogen.UpdateItemInput
		if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
			return
		}
		out, err := h.service.UpdateItem(batch, item, in)
		if err != nil {
			writeVideoAPIError(w, err)
			return
		}
		writeJSON(w, 200, out)
	case http.MethodDelete:
		if err := h.service.DeleteItem(batch, item); err != nil {
			writeVideoAPIError(w, err)
			return
		}
		w.WriteHeader(204)
	default:
		methodNotAllowed(w, "PUT, DELETE")
	}
}
func (h videoBatchHandler) moveItem(w http.ResponseWriter, r *http.Request, batch, item string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct {
		Direction int `json:"direction"`
	}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	if in.Direction != -1 && in.Direction != 1 {
		writeAPIError(w, 400, "invalid_move", "direction must be -1 or 1")
		return
	}
	out, err := h.service.MoveItem(batch, item, in.Direction)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	writeJSON(w, 200, out)
}
func (h videoBatchHandler) detail(batch videogen.Batch) videoBatchResponse {
	available := false
	cfg := h.config.Snapshot().Videos
	if batch.ExecutionKind == "http" {
		for _, p := range cfg.HTTPProviders {
			if p.ID == batch.PresetID && p.Enabled {
				available = true
			}
		}
	} else {
		for _, p := range cfg.CLIPresets {
			if p.ID == batch.PresetID && p.Enabled {
				available = true
			}
		}
	}
	ids := map[string]struct{}{}
	add := func(id string) {
		if id != "" {
			ids[id] = struct{}{}
		}
	}
	for _, it := range batch.Items {
		add(it.InitImageID)
		add(it.EndImageID)
		for _, id := range it.ControlFrameIDs {
			add(id)
		}
		for _, a := range it.SelectedAssets {
			add(a.AssetID)
		}
		for _, a := range it.Attempts {
			for _, input := range a.Snapshot.InputAssets {
				add(input.ID)
			}
			add(a.OutputAssetID)
		}
	}
	assets := make([]videoAssetSummary, 0, len(ids))
	for id := range ids {
		if a, ok := h.assets.Get(id); ok {
			assets = append(assets, videoAssetSummary{ID: a.ID, SHA256: a.SHA256, MediaType: a.MediaType, DisplayName: a.DisplayName, Size: a.Size, State: a.State})
		}
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	return videoBatchResponse{Batch: batch, PresetAvailable: available, Assets: assets}
}
func writeVideoAPIError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		writeAPIError(w, 504, "provider_timeout", "video provider request timed out")
	case isVideoUpstreamError(err):
		writeAPIError(w, 502, "provider_error", "video provider request failed")
	case errors.Is(err, asset.ErrNotFound), errors.Is(err, videogen.ErrBatchNotFound), errors.Is(err, videogen.ErrItemNotFound), errors.Is(err, videogen.ErrAttemptNotFound), errors.Is(err, videogen.ErrTailExtractionNotFound), errors.Is(err, videogen.ErrVideoAssetNotFound), errors.Is(err, videogen.ErrCLIAttemptNotFound), errors.Is(err, videogen.ErrVideoPresetNotFound):
		writeAPIError(w, 404, "not_found", err.Error())
	case errors.Is(err, videogen.ErrActiveAttempt):
		writeAPIError(w, 409, "active_attempt", err.Error())
	case errors.Is(err, videogen.ErrMoveBoundary):
		writeAPIError(w, 409, "move_boundary", err.Error())
	case errors.Is(err, videogen.ErrVideoManagerClosed), errors.Is(err, videogen.ErrTailExtractorClosed):
		writeAPIError(w, 409, "manager_closed", "video workbench is shutting down")
	default:
		var pe *os.PathError
		if errors.As(err, &pe) {
			writeAPIError(w, 500, "storage_error", "video data could not be persisted")
		} else {
			writeAPIError(w, 400, "invalid_video", err.Error())
		}
	}
}

func isVideoUpstreamError(err error) bool {
	var upstream *sdcpp.HTTPError
	return errors.As(err, &upstream)
}
