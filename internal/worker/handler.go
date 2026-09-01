package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type Handler struct {
	version string
	manager *Manager
}

func NewHandler(version string, manager *Manager) http.Handler {
	return Handler{version: version, manager: manager}
}

func (handler Handler) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	switch request.URL.Path {
	case "/api/v1/health":
		if handler.requireMethod(response, request, http.MethodGet) {
			handler.health(response)
		}
		return
	case "/api/v1/process":
		if handler.requireMethod(response, request, http.MethodGet) {
			handler.status(response)
		}
		return
	case "/api/v1/process/start":
		if handler.requireMethod(response, request, http.MethodPost) {
			handler.start(response, request)
		}
		return
	}
	segments := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(segments) >= 5 && segments[0] == "api" && segments[1] == "v1" && segments[2] == "process" && segments[3] != "" {
		runID := segments[3]
		switch {
		case len(segments) == 5 && segments[4] == "stop":
			if handler.requireMethod(response, request, http.MethodPost) {
				handler.stop(response, request, runID)
			}
			return
		case len(segments) == 5 && segments[4] == "logs":
			if handler.requireMethod(response, request, http.MethodGet) {
				handler.logs(response, runID)
			}
			return
		case len(segments) == 6 && segments[4] == "logs" && segments[5] == "events":
			if handler.requireMethod(response, request, http.MethodGet) {
				handler.logEvents(response, request, runID)
			}
			return
		}
	}
	writeWorkerError(response, http.StatusNotFound, "not_found", "resource not found")
}

func (handler Handler) health(response http.ResponseWriter) {
	writeWorkerJSON(response, http.StatusOK, HealthResponse{
		Status:     "ok",
		Version:    handler.version,
		InstanceID: handler.manager.instanceID,
	})
}

func (handler Handler) status(response http.ResponseWriter) {
	writeWorkerJSON(response, http.StatusOK, handler.manager.Status())
}

func (handler Handler) start(response http.ResponseWriter, request *http.Request) {
	var input StartRequest
	if !decodeWorkerJSON(response, request, &input) {
		return
	}
	run, err := handler.manager.Start(request.Context(), input)
	if err != nil {
		handler.writeManagerError(response, err)
		return
	}
	writeWorkerJSON(response, http.StatusAccepted, run)
}

func (handler Handler) stop(response http.ResponseWriter, request *http.Request, runID string) {
	run, err := handler.manager.Stop(request.Context(), runID)
	if err != nil {
		handler.writeManagerError(response, err)
		return
	}
	writeWorkerJSON(response, http.StatusOK, run)
}

func (handler Handler) logs(response http.ResponseWriter, runID string) {
	snapshot, err := handler.manager.LogSnapshot(runID)
	if err != nil {
		handler.writeManagerError(response, err)
		return
	}
	response.Header().Set("Content-Type", "text/plain; charset=utf-8")
	response.Header().Set("X-Log-Start-Offset", strconv.FormatInt(snapshot.StartOffset, 10))
	response.Header().Set("X-Log-End-Offset", strconv.FormatInt(snapshot.EndOffset, 10))
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(snapshot.Data)
}

func (handler Handler) logEvents(response http.ResponseWriter, request *http.Request, runID string) {
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeWorkerError(response, http.StatusInternalServerError, "stream_unsupported", "streaming is unavailable")
		return
	}
	snapshot, chunks, cancel, err := handler.manager.SubscribeLog(runID)
	if err != nil {
		handler.writeManagerError(response, err)
		return
	}
	defer cancel()
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	writeWorkerSSE(response, "snapshot", snapshot.StartOffset, snapshot.Data)
	flusher.Flush()
	for {
		select {
		case chunk, open := <-chunks:
			if !open {
				return
			}
			writeWorkerSSE(response, "chunk", chunk.Offset, chunk.Data)
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func (handler Handler) requireMethod(response http.ResponseWriter, request *http.Request, method string) bool {
	if request.Method == method {
		return true
	}
	response.Header().Set("Allow", method)
	writeWorkerError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	return false
}

func (handler Handler) writeManagerError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrSlotBusy):
		writeWorkerError(response, http.StatusConflict, "slot_busy", err.Error())
	case errors.Is(err, ErrRunMismatch):
		writeWorkerError(response, http.StatusConflict, "run_mismatch", err.Error())
	case errors.Is(err, ErrNoRun):
		writeWorkerError(response, http.StatusNotFound, "no_run", err.Error())
	default:
		writeWorkerError(response, http.StatusBadRequest, "invalid_process", truncateError(err.Error()))
	}
}

func decodeWorkerJSON(response http.ResponseWriter, request *http.Request, target any) bool {
	request.Body = http.MaxBytesReader(response, request.Body, MaxRequestBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeWorkerError(response, http.StatusRequestEntityTooLarge, "request_too_large", "request body is too large")
			return false
		}
		writeWorkerError(response, http.StatusBadRequest, "invalid_json", "request body must be one valid JSON object")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeWorkerError(response, http.StatusBadRequest, "invalid_json", "request body must contain one JSON value")
		return false
	}
	return true
}

func writeWorkerJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeWorkerError(response http.ResponseWriter, status int, code, message string) {
	writeWorkerJSON(response, status, ErrorEnvelope{Error: APIError{Code: code, Message: message}})
}

func writeWorkerSSE(response io.Writer, kind string, offset int64, data []byte) {
	payload, _ := json.Marshal(struct {
		Offset int64  `json:"offset"`
		Data   []byte `json:"data"`
	}{Offset: offset, Data: data})
	_, _ = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", kind, payload)
}
