package web

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

type ImageCapabilitiesClient interface {
	Capabilities(context.Context, sdcpp.ImageProvider) (sdcpp.Capabilities, error)
}

type imageConfigHandler struct {
	repository   *config.Repository
	capabilities ImageCapabilitiesClient
	maxBody      int64
}

func (handler imageConfigHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/images"), "/")
	if path == "config" {
		handler.config(response, request)
		return
	}
	segments := strings.Split(path, "/")
	if len(segments) == 3 && segments[0] == "providers" && segments[1] != "" && segments[2] == "capabilities" {
		handler.providerCapabilities(response, request, segments[1])
		return
	}
	writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
}

func (handler imageConfigHandler) config(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, handler.repository.Snapshot().Images)
	case http.MethodPut:
		var configuration sdcpp.ImageConfig
		if !decodeStrictJSON(response, request, handler.maxBody, &configuration, false) {
			return
		}
		updated, err := handler.repository.UpdateImages(configuration)
		if err != nil {
			var pathError *os.PathError
			if errors.As(err, &pathError) {
				writeAPIError(response, http.StatusInternalServerError, "storage_error", "image configuration could not be persisted")
				return
			}
			writeAPIError(response, http.StatusBadRequest, "invalid_image_config", err.Error())
			return
		}
		writeJSON(response, http.StatusOK, updated.Images)
	default:
		methodNotAllowed(response, http.MethodGet+", "+http.MethodPut)
	}
}

func (handler imageConfigHandler) providerCapabilities(response http.ResponseWriter, request *http.Request, providerID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	var provider sdcpp.ImageProvider
	found := false
	for _, candidate := range handler.repository.Snapshot().Images.Providers {
		if candidate.ID == providerID {
			provider, found = candidate, true
			break
		}
	}
	if !found {
		writeAPIError(response, http.StatusNotFound, "not_found", "image provider not found")
		return
	}
	if !provider.Enabled {
		writeAPIError(response, http.StatusBadRequest, "provider_disabled", "image provider is disabled")
		return
	}
	if handler.capabilities == nil {
		writeAPIError(response, http.StatusServiceUnavailable, "capabilities_unavailable", "image capabilities client is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), time.Duration(provider.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	capabilities, err := handler.capabilities.Capabilities(ctx, provider)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeAPIError(response, http.StatusGatewayTimeout, "provider_timeout", "image provider request timed out")
			return
		}
		writeAPIError(response, http.StatusBadGateway, "provider_error", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, capabilities)
}
