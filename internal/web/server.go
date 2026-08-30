package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/imagegen"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/llm"
	"github.com/ekk1/ai-desktop/internal/session"
)

//go:embed static/*
var staticFiles embed.FS

type Options struct {
	Version           string
	DataDir           string
	Config            config.Config
	BackendRepository *backend.Repository
	BackendManager    *backend.Manager
	AssetRepository   *asset.Repository
	KnowledgeService  *knowledge.Service
	ConfigRepository  *config.Repository
	SessionService    *session.Service
	LLMManager        *llm.Manager
	ExaService        *llm.ExaService
	ImageCapabilities ImageCapabilitiesClient
	ImageService      *imagegen.Service
	ImageManager      *imagegen.Manager
}

type errorEnvelope struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func NewHandler(options Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", requireMethod(http.MethodGet, func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}{Status: "ok", Version: options.Version})
	}))
	mux.HandleFunc("/api/v1/settings", requireMethod(http.MethodGet, func(response http.ResponseWriter, _ *http.Request) {
		writeJSON(response, http.StatusOK, struct {
			DataDir                string `json:"data_dir"`
			ListenPort             int    `json:"listen_port"`
			ShutdownTimeoutSeconds int    `json:"shutdown_timeout_seconds"`
			MaxUploadBytes         int64  `json:"max_upload_bytes"`
		}{
			DataDir:                options.DataDir,
			ListenPort:             options.Config.ListenPort,
			ShutdownTimeoutSeconds: options.Config.ShutdownTimeoutSeconds,
			MaxUploadBytes:         options.Config.MaxUploadBytes,
		})
	}))
	mux.HandleFunc("/api/v1/", func(response http.ResponseWriter, _ *http.Request) {
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	})
	if options.BackendRepository != nil && options.BackendManager != nil {
		backendAPI := backendHandler{
			repository: options.BackendRepository,
			manager:    options.BackendManager,
			maxBody:    options.Config.MaxUploadBytes,
		}
		mux.HandleFunc("/api/v1/backends", backendAPI.serve)
		mux.HandleFunc("/api/v1/backends/", backendAPI.serve)
	}
	if options.AssetRepository != nil {
		assetAPI := assetHandler{repository: options.AssetRepository, maxBody: options.Config.MaxUploadBytes}
		mux.HandleFunc("/api/v1/assets", assetAPI.serve)
		mux.HandleFunc("/api/v1/assets/", assetAPI.serve)
	}
	if options.ImageService != nil && options.AssetRepository != nil && options.ConfigRepository != nil {
		imageAPI := imageBatchHandler{
			service: options.ImageService, assets: options.AssetRepository, config: options.ConfigRepository,
			manager: options.ImageManager, maxBody: options.Config.MaxUploadBytes,
		}
		mux.HandleFunc("/api/v1/images/batches", imageAPI.serve)
		mux.HandleFunc("/api/v1/images/batches/", imageAPI.serve)
	}
	if options.ImageManager != nil {
		attemptAPI := imageAttemptHandler{manager: options.ImageManager, maxBody: options.Config.MaxUploadBytes}
		mux.HandleFunc("/api/v1/images/attempts/", attemptAPI.serve)
	}
	if options.KnowledgeService != nil {
		knowledgeAPI := knowledgeHandler{service: options.KnowledgeService, maxBody: options.Config.MaxUploadBytes}
		mux.HandleFunc("/api/v1/knowledge", knowledgeAPI.serve)
		mux.HandleFunc("/api/v1/knowledge/", knowledgeAPI.serve)
	}
	if options.ConfigRepository != nil {
		configurationAPI := llmConfigHandler{repository: options.ConfigRepository, maxBody: options.Config.MaxUploadBytes}
		mux.HandleFunc("/api/v1/llm/config", configurationAPI.serve)
		mux.HandleFunc("/api/v1/llm/providers/", configurationAPI.serve)
		imageConfigurationAPI := imageConfigHandler{
			repository: options.ConfigRepository, capabilities: options.ImageCapabilities, maxBody: options.Config.MaxUploadBytes,
		}
		mux.HandleFunc("/api/v1/images/config", imageConfigurationAPI.serve)
		mux.HandleFunc("/api/v1/images/providers/", imageConfigurationAPI.serve)
	}
	if options.SessionService != nil {
		var runAPI *llmRunHandler
		if options.LLMManager != nil {
			handler := llmRunHandler{manager: options.LLMManager, maxBody: options.Config.MaxUploadBytes}
			runAPI = &handler
			mux.HandleFunc("/api/v1/llm/runs/", handler.serve)
		}
		var exaAPI *llmExaHandler
		if options.ExaService != nil {
			handler := llmExaHandler{service: options.ExaService, maxBody: options.Config.MaxUploadBytes}
			exaAPI = &handler
		}
		sessionAPI := llmSessionHandler{
			service: options.SessionService, maxBody: options.Config.MaxUploadBytes, runs: runAPI, exa: exaAPI,
		}
		mux.HandleFunc("/api/v1/llm/sessions", sessionAPI.serve)
		mux.HandleFunc("/api/v1/llm/sessions/", sessionAPI.serve)
	}

	serveEmbeddedFile(mux, "/assets/styles.css", "static/styles.css", "text/css; charset=utf-8")
	serveEmbeddedFile(mux, "/assets/app.js", "static/app.js", "text/javascript; charset=utf-8")
	serveEmbeddedFile(mux, "/assets/llm-config.js", "static/llm-config.js", "text/javascript; charset=utf-8")
	serveEmbeddedFile(mux, "/assets/llm.js", "static/llm.js", "text/javascript; charset=utf-8")
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(response, request)
			return
		}
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		contents, err := fs.ReadFile(staticFiles, "static/index.html")
		if err != nil {
			http.Error(response, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = response.Write(contents)
	})

	return securityHeaders(mux)
}

func requireMethod(method string, next http.HandlerFunc) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		if request.Method != method {
			response.Header().Set("Allow", method)
			writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
			return
		}
		next(response, request)
	}
}

func serveEmbeddedFile(mux *http.ServeMux, route, path, contentType string) {
	mux.HandleFunc(route, func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet && request.Method != http.MethodHead {
			response.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
			http.Error(response, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
			return
		}
		contents, err := fs.ReadFile(staticFiles, path)
		if err != nil {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", contentType)
		response.Header().Set("Cache-Control", "no-cache")
		_, _ = response.Write(contents)
	})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(response, request)
	})
}

func writeAPIError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, errorEnvelope{Error: apiError{Code: code, Message: message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func decodeStrictJSON(response http.ResponseWriter, request *http.Request, maxBody int64, target any, allowEmpty bool) bool {
	request.Body = http.MaxBytesReader(response, request.Body, maxBody)
	decoder := json.NewDecoder(request.Body)
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		if allowEmpty && errors.Is(err, io.EOF) {
			return true
		}
		writeAPIError(response, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object")
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		writeAPIError(response, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(response, http.StatusBadRequest, "invalid_json", "request body must contain one JSON object")
		return false
	}
	payload := json.NewDecoder(bytes.NewReader(raw))
	payload.DisallowUnknownFields()
	if err := payload.Decode(target); err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object")
		return false
	}
	return true
}

func methodNotAllowed(response http.ResponseWriter, allow string) {
	response.Header().Set("Allow", allow)
	writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}
