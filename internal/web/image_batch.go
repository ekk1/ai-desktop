package web

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/imagegen"
)

type imageBatchResponse struct {
	Batch             imagegen.Batch `json:"batch"`
	ProviderAvailable bool           `json:"provider_available"`
	Assets            []asset.Asset  `json:"assets"`
}

type imageBatchHandler struct {
	service *imagegen.Service
	assets  *asset.Repository
	config  *config.Repository
	manager *imagegen.Manager
	maxBody int64
}

func (handler imageBatchHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/images/batches"), "/")
	if path == "" {
		handler.collection(response, request)
		return
	}
	segments := strings.Split(path, "/")
	if !validLLMID(segments[0]) {
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	batchID := segments[0]
	switch {
	case len(segments) == 1:
		handler.batch(response, request, batchID)
	case len(segments) == 2 && segments[1] == "execute" && handler.manager != nil:
		imageAttemptHandler{manager: handler.manager, maxBody: handler.maxBody}.executeBatch(response, request, batchID)
	case len(segments) == 2 && segments[1] == "events" && handler.manager != nil:
		imageAttemptHandler{manager: handler.manager, maxBody: handler.maxBody}.events(response, request, batchID)
	case len(segments) == 2 && segments[1] == "items":
		handler.createItems(response, request, batchID)
	case len(segments) == 3 && segments[1] == "items" && validLLMID(segments[2]):
		handler.item(response, request, batchID, segments[2])
	case len(segments) == 4 && segments[1] == "items" && validLLMID(segments[2]) && segments[3] == "execute" && handler.manager != nil:
		imageAttemptHandler{manager: handler.manager, maxBody: handler.maxBody}.executeItem(response, request, batchID, segments[2])
	case len(segments) == 4 && segments[1] == "items" && validLLMID(segments[2]) && segments[3] == "move":
		handler.moveItem(response, request, batchID, segments[2])
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler imageBatchHandler) collection(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, struct {
			Batches []imagegen.Batch `json:"batches"`
		}{Batches: handler.service.List(imagegen.Filter{
			Folder: request.URL.Query().Get("folder"), Query: request.URL.Query().Get("q"), ProviderID: request.URL.Query().Get("provider_id"),
		})})
	case http.MethodPost:
		var input imagegen.CreateBatchInput
		if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
			return
		}
		created, err := handler.service.CreateBatch(input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, created)
	default:
		methodNotAllowed(response, http.MethodGet+", "+http.MethodPost)
	}
}

func (handler imageBatchHandler) batch(response http.ResponseWriter, request *http.Request, batchID string) {
	switch request.Method {
	case http.MethodGet:
		batch, ok := handler.service.Get(batchID)
		if !ok {
			handler.writeError(response, imagegen.ErrBatchNotFound)
			return
		}
		writeJSON(response, http.StatusOK, handler.detail(batch))
	case http.MethodPut:
		var input imagegen.UpdateBatchInput
		if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
			return
		}
		updated, err := handler.service.UpdateBatch(batchID, input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
	case http.MethodDelete:
		batch, ok := handler.service.Get(batchID)
		if !ok {
			handler.writeError(response, imagegen.ErrBatchNotFound)
			return
		}
		if batchHasActiveAttempt(batch) {
			handler.writeError(response, imagegen.ErrActiveAttempt)
			return
		}
		if err := handler.service.DeleteBatch(batchID); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response, http.MethodGet+", "+http.MethodPut+", "+http.MethodDelete)
	}
}

func (handler imageBatchHandler) createItems(response http.ResponseWriter, request *http.Request, batchID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		Items []imagegen.CreateItemInput `json:"items"`
	}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	created, err := handler.service.CreateItems(batchID, input.Items)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, struct {
		Items []imagegen.Item `json:"items"`
	}{Items: created})
}

func (handler imageBatchHandler) item(response http.ResponseWriter, request *http.Request, batchID, itemID string) {
	switch request.Method {
	case http.MethodPut:
		var input imagegen.UpdateItemInput
		if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
			return
		}
		updated, err := handler.service.UpdateItem(batchID, itemID, input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
	case http.MethodDelete:
		batch, ok := handler.service.Get(batchID)
		if !ok {
			handler.writeError(response, imagegen.ErrBatchNotFound)
			return
		}
		if itemHasActiveAttempt(batch, itemID) {
			handler.writeError(response, imagegen.ErrActiveAttempt)
			return
		}
		if err := handler.service.DeleteItem(batchID, itemID); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response, http.MethodPut+", "+http.MethodDelete)
	}
}

func (handler imageBatchHandler) moveItem(response http.ResponseWriter, request *http.Request, batchID, itemID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		Direction int `json:"direction"`
	}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	if input.Direction != -1 && input.Direction != 1 {
		writeAPIError(response, http.StatusBadRequest, "invalid_move", "direction must be -1 or 1")
		return
	}
	updated, err := handler.service.MoveItem(batchID, itemID, input.Direction)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (handler imageBatchHandler) detail(batch imagegen.Batch) imageBatchResponse {
	available := false
	for _, provider := range handler.config.Snapshot().Images.Providers {
		if provider.ID == batch.ProviderID && provider.Enabled {
			available = true
			break
		}
	}
	ids := make([]string, 0)
	seen := make(map[string]struct{})
	add := func(id string) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	for _, item := range batch.Items {
		add(item.InputAssets.InitImageID)
		for _, id := range item.InputAssets.RefImageIDs {
			add(id)
		}
		add(item.InputAssets.MaskImageID)
		add(item.InputAssets.ControlImageID)
		add(item.InputAssets.IPAdapterImageID)
		for _, attempt := range item.Attempts {
			for _, id := range attempt.ResultAssetIDs {
				add(id)
			}
		}
	}
	assets := make([]asset.Asset, 0, len(ids))
	for _, id := range ids {
		if stored, ok := handler.assets.Get(id); ok {
			assets = append(assets, stored)
		}
	}
	return imageBatchResponse{Batch: batch, ProviderAvailable: available, Assets: assets}
}

func (handler imageBatchHandler) writeError(response http.ResponseWriter, err error) {
	writeImageAPIError(response, err)
}

func writeImageAPIError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, imagegen.ErrBatchNotFound), errors.Is(err, imagegen.ErrItemNotFound), errors.Is(err, imagegen.ErrAttemptNotFound), errors.Is(err, imagegen.ErrImageProviderNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, imagegen.ErrActiveAttempt):
		writeAPIError(response, http.StatusConflict, "active_attempt", err.Error())
	case errors.Is(err, imagegen.ErrMoveBoundary):
		writeAPIError(response, http.StatusConflict, "move_boundary", err.Error())
	case errors.Is(err, imagegen.ErrImageAssetNotFound), errors.Is(err, imagegen.ErrImageAssetType):
		writeAPIError(response, http.StatusBadRequest, "invalid_reference", err.Error())
	case errors.Is(err, imagegen.ErrImageProviderDisabled):
		writeAPIError(response, http.StatusBadRequest, "provider_disabled", err.Error())
	case errors.Is(err, imagegen.ErrImageManagerClosed):
		writeAPIError(response, http.StatusServiceUnavailable, "manager_closed", err.Error())
	default:
		var pathError *os.PathError
		if errors.As(err, &pathError) {
			writeAPIError(response, http.StatusInternalServerError, "storage_error", "image data could not be persisted")
			return
		}
		writeAPIError(response, http.StatusBadRequest, "invalid_image", err.Error())
	}
}

func batchHasActiveAttempt(batch imagegen.Batch) bool {
	for _, item := range batch.Items {
		if itemHasActiveAttempt(batch, item.ID) {
			return true
		}
	}
	return false
}

func itemHasActiveAttempt(batch imagegen.Batch, itemID string) bool {
	for _, item := range batch.Items {
		if item.ID != itemID {
			continue
		}
		for _, attempt := range item.Attempts {
			if attempt.State == imagegen.AttemptQueued || attempt.State == imagegen.AttemptSubmitting || attempt.State == imagegen.AttemptPolling {
				return true
			}
		}
	}
	return false
}
