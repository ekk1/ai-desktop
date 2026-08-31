package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/backend"
	"github.com/ekk1/ai-desktop/internal/worker"
)

type backendHandler struct {
	repository *backend.Repository
	manager    *backend.Manager
	maxBody    int64
}

func (handler backendHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v1/backends")
	path = strings.Trim(path, "/")
	if path == "" {
		handler.serveCollection(response, request)
		return
	}
	if path == "worker/test" {
		handler.testWorker(response, request)
		return
	}
	segments := strings.Split(path, "/")
	id := segments[0]
	if len(segments) == 1 {
		handler.serveProfile(response, request, id)
		return
	}
	action := strings.Join(segments[1:], "/")
	switch action {
	case "start":
		handler.start(response, request, id)
	case "stop":
		handler.stop(response, request, id)
	case "runs":
		handler.run(response, request, id)
	case "logs":
		handler.logs(response, request, id)
	case "logs/events":
		handler.logEvents(response, request, id)
	case "logs/save":
		handler.saveLog(response, request, id)
	case "logs/clear":
		handler.clearLog(response, request, id)
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler backendHandler) testWorker(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		handler.methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		WorkerBaseURL string `json:"worker_base_url"`
	}
	if !handler.decode(response, request, &input, false) {
		return
	}
	execution := backend.Execution{Kind: backend.ExecutionWorker, WorkerBaseURL: input.WorkerBaseURL}
	if err := execution.Validate(); err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_worker_url", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 3*time.Second)
	defer cancel()
	health, err := (worker.Client{BaseURL: input.WorkerBaseURL, MaxResponseBytes: 1 << 20}).Health(ctx)
	if err != nil {
		writeAPIError(response, http.StatusBadGateway, "worker_unreachable", err.Error())
		return
	}
	writeJSON(response, http.StatusOK, health)
}

func (handler backendHandler) serveCollection(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, struct {
			Profiles []backend.Profile `json:"profiles"`
			Runs     []backend.RunInfo `json:"runs"`
		}{Profiles: handler.repository.List(), Runs: handler.manager.Runs()})
	case http.MethodPost:
		var profile backend.Profile
		if !handler.decode(response, request, &profile, false) {
			return
		}
		profile.ID = ""
		created, err := handler.repository.Create(profile)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, created)
	default:
		handler.methodNotAllowed(response, http.MethodGet+", "+http.MethodPost)
	}
}

func (handler backendHandler) serveProfile(response http.ResponseWriter, request *http.Request, id string) {
	switch request.Method {
	case http.MethodGet:
		profile, ok := handler.repository.Get(id)
		if !ok {
			handler.writeError(response, backend.ErrNotFound)
			return
		}
		run, _ := handler.manager.Run(id)
		writeJSON(response, http.StatusOK, struct {
			Profile backend.Profile `json:"profile"`
			Run     backend.RunInfo `json:"run"`
		}{Profile: profile, Run: run})
	case http.MethodPut:
		var profile backend.Profile
		if !handler.decode(response, request, &profile, false) {
			return
		}
		updated, err := handler.repository.Update(id, profile)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
	case http.MethodDelete:
		if run, ok := handler.manager.Run(id); ok && (run.State == backend.StateStarting || run.State == backend.StateRunning || run.State == backend.StateStopping) {
			handler.writeError(response, backend.ErrRunning)
			return
		}
		if err := handler.repository.Delete(id); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		handler.methodNotAllowed(response, http.MethodGet+", "+http.MethodPut+", "+http.MethodDelete)
	}
}

func (handler backendHandler) start(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPost {
		handler.methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		Variables map[string]string `json:"variables"`
	}
	if !handler.decode(response, request, &input, true) {
		return
	}
	run, err := handler.manager.Start(request.Context(), id, input.Variables)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, run)
}

func (handler backendHandler) stop(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPost {
		handler.methodNotAllowed(response, http.MethodPost)
		return
	}
	if err := handler.manager.Stop(request.Context(), id); err != nil {
		handler.writeError(response, err)
		return
	}
	run, _ := handler.manager.Run(id)
	writeJSON(response, http.StatusOK, run)
}

func (handler backendHandler) run(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet {
		handler.methodNotAllowed(response, http.MethodGet)
		return
	}
	run, ok := handler.manager.Run(id)
	if !ok {
		handler.writeError(response, backend.ErrNotRunning)
		return
	}
	writeJSON(response, http.StatusOK, run)
}

func (handler backendHandler) logs(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet {
		handler.methodNotAllowed(response, http.MethodGet)
		return
	}
	contents, err := handler.manager.LogSnapshot(id)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Log string `json:"log"`
	}{Log: string(contents)})
}

func (handler backendHandler) logEvents(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet {
		handler.methodNotAllowed(response, http.MethodGet)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, "stream_unsupported", "streaming is unavailable")
		return
	}
	snapshot, chunks, cancel, err := handler.manager.SubscribeLog(id)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	defer cancel()
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	writeBackendLogSSE(response, "snapshot", snapshot.StartOffset, snapshot.Data)
	flusher.Flush()
	for {
		select {
		case chunk, open := <-chunks:
			if !open {
				return
			}
			writeBackendLogSSE(response, "chunk", chunk.Offset, chunk.Data)
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (handler backendHandler) saveLog(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPost {
		handler.methodNotAllowed(response, http.MethodPost)
		return
	}
	path, err := handler.manager.SaveLog(id)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Path string `json:"path"`
	}{Path: path})
}

func (handler backendHandler) clearLog(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPost {
		handler.methodNotAllowed(response, http.MethodPost)
		return
	}
	if err := handler.manager.ClearLog(id); err != nil {
		handler.writeError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (handler backendHandler) decode(response http.ResponseWriter, request *http.Request, target any, allowEmpty bool) bool {
	return decodeStrictJSON(response, request, handler.maxBody, target, allowEmpty)
}

func (handler backendHandler) methodNotAllowed(response http.ResponseWriter, allow string) {
	response.Header().Set("Allow", allow)
	writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func (handler backendHandler) writeError(response http.ResponseWriter, err error) {
	var workerError *worker.ClientError
	switch {
	case errors.Is(err, backend.ErrNotFound), errors.Is(err, backend.ErrNotRunning):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, backend.ErrConflict), errors.Is(err, backend.ErrRunning):
		writeAPIError(response, http.StatusConflict, "conflict", err.Error())
	case errors.As(err, &workerError) && workerError.StatusCode == http.StatusConflict:
		writeAPIError(response, http.StatusConflict, "worker_conflict", workerError.Message)
	default:
		writeAPIError(response, http.StatusBadRequest, "invalid_backend", err.Error())
	}
}

func writeBackendLogSSE(response io.Writer, event string, startOffset int64, contents []byte) {
	encoded, _ := json.Marshal(struct {
		StartOffset int64  `json:"start_offset"`
		EndOffset   int64  `json:"end_offset"`
		DataBase64  string `json:"data_base64"`
	}{
		StartOffset: startOffset,
		EndOffset:   startOffset + int64(len(contents)),
		DataBase64:  base64.StdEncoding.EncodeToString(contents),
	})
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event, encoded)
}
