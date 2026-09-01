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
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

// VideoCapabilitiesClient intentionally shares the image capability protocol.
type VideoCapabilitiesClient interface {
	Capabilities(context.Context, sdcpp.ImageProvider) (sdcpp.Capabilities, error)
}

type videoConfigHandler struct {
	repository   *config.Repository
	capabilities VideoCapabilitiesClient
	maxBody      int64
}

func (h videoConfigHandler) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/videos"), "/")
	if p == "config" {
		h.config(w, r)
		return
	}
	s := strings.Split(p, "/")
	if len(s) == 3 && s[0] == "providers" && s[1] != "" && s[2] == "capabilities" {
		h.providerCapabilities(w, r, s[1])
		return
	}
	writeAPIError(w, http.StatusNotFound, "not_found", "resource not found")
}

func (h videoConfigHandler) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, h.repository.Snapshot().Videos)
	case http.MethodPut:
		var videos videoconfig.Config
		if !decodeStrictJSON(w, r, h.maxBody, &videos, false) {
			return
		}
		updated, err := h.repository.UpdateVideos(videos)
		if err != nil {
			var pe *os.PathError
			if errors.As(err, &pe) {
				writeAPIError(w, 500, "storage_error", "video configuration could not be persisted")
			} else {
				writeAPIError(w, 400, "invalid_video_config", err.Error())
			}
			return
		}
		writeJSON(w, http.StatusOK, updated.Videos)
	default:
		methodNotAllowed(w, http.MethodGet+", "+http.MethodPut)
	}
}

func (h videoConfigHandler) providerCapabilities(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	var found *videoconfig.HTTPProvider
	for _, p := range h.repository.Snapshot().Videos.HTTPProviders {
		if p.ID == id {
			p := p
			found = &p
			break
		}
	}
	if found == nil {
		writeAPIError(w, 404, "not_found", "video provider not found")
		return
	}
	if !found.Enabled {
		writeAPIError(w, 400, "provider_disabled", "video provider is disabled")
		return
	}
	if h.capabilities == nil {
		writeAPIError(w, 503, "capabilities_unavailable", "video capabilities client is unavailable")
		return
	}
	// Capabilities only needs the HTTP transport shape.  Clamp its response
	// buffer to a valid, safe value; no saved image-provider state is used.
	limit := found.MaxErrorBytes
	if limit < 1024 {
		limit = 1024
	}
	if limit > 1<<30 {
		limit = 1 << 30
	}
	provider := sdcpp.ImageProvider{ID: found.ID, Name: found.Name, BaseURL: found.BaseURL, Headers: found.Headers, ConnectTimeoutSeconds: found.ConnectTimeoutSeconds, JobTimeoutSeconds: found.JobTimeoutSeconds, PollIntervalMilliseconds: found.PollIntervalMilliseconds, MaxResponseBytes: limit, MaxImageBytes: limit, MaxConcurrentJobs: found.MaxConcurrentJobs, Enabled: true}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(found.ConnectTimeoutSeconds)*time.Second)
	defer cancel()
	capabilities, err := h.capabilities.Capabilities(ctx, provider)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			writeAPIError(w, 504, "provider_timeout", "video provider request timed out")
		} else {
			writeAPIError(w, 502, "provider_error", err.Error())
		}
		return
	}
	// Preserve all mode-specific fields and state explicitly whether vid_gen is available.
	available := capabilities.CurrentMode == "vid_gen"
	for _, mode := range capabilities.SupportedModes {
		if mode == "vid_gen" {
			available = true
			break
		}
	}
	writeJSON(w, http.StatusOK, struct {
		sdcpp.Capabilities
		VideoGenerationSupported bool `json:"video_generation_supported"`
	}{Capabilities: capabilities, VideoGenerationSupported: available})
}
