package web

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/config"
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

	serveEmbeddedFile(mux, "/assets/styles.css", "static/styles.css", "text/css; charset=utf-8")
	serveEmbeddedFile(mux, "/assets/app.js", "static/app.js", "text/javascript; charset=utf-8")
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
