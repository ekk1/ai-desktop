package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/llm"
)

type llmRunHandler struct {
	manager *llm.Manager
	maxBody int64
}

func (handler llmRunHandler) execute(response http.ResponseWriter, request *http.Request, sessionID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct {
		PanelID      string   `json:"panel_id"`
		QuickPathIDs []string `json:"quick_path_ids"`
	}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	if !validLLMID(input.PanelID) {
		writeAPIError(response, http.StatusBadRequest, "invalid_panel", "panel_id is invalid")
		return
	}
	runs, err := handler.manager.Start(sessionID, input.PanelID, input.QuickPathIDs)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, struct {
		Runs []llm.Run `json:"runs"`
	}{Runs: runs})
}

func (handler llmRunHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/llm/runs"), "/")
	segments := strings.Split(path, "/")
	if len(segments) == 0 || !validLLMID(segments[0]) {
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	runID := segments[0]
	switch {
	case len(segments) == 1:
		handler.item(response, request, runID)
	case len(segments) == 2 && segments[1] == "cancel":
		handler.cancel(response, request, runID)
	case len(segments) == 2 && segments[1] == "events":
		handler.events(response, request, runID)
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler llmRunHandler) item(response http.ResponseWriter, request *http.Request, runID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	run, exists := handler.manager.Get(runID)
	if !exists {
		handler.writeError(response, llm.ErrRunNotFound)
		return
	}
	writeJSON(response, http.StatusOK, run)
}

func (handler llmRunHandler) cancel(response http.ResponseWriter, request *http.Request, runID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, true) {
		return
	}
	err := handler.manager.Cancel(runID)
	if errors.Is(err, llm.ErrRunNotActive) {
		run, exists := handler.manager.Get(runID)
		if !exists {
			handler.writeError(response, llm.ErrRunNotFound)
			return
		}
		writeJSON(response, http.StatusOK, run)
		return
	}
	if err != nil {
		handler.writeError(response, err)
		return
	}
	run, _ := handler.manager.Get(runID)
	writeJSON(response, http.StatusAccepted, run)
}

func (handler llmRunHandler) events(response http.ResponseWriter, request *http.Request, runID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, "stream_unavailable", "streaming is unavailable")
		return
	}
	events, unsubscribe, err := handler.manager.Subscribe(runID)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	defer unsubscribe()
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			if err := writeRunEvent(response, event); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(response, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-request.Context().Done():
			return
		}
	}
}

func writeRunEvent(response http.ResponseWriter, event llm.RunEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event.Type, data)
	return err
}

func (handler llmRunHandler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, llm.ErrRunNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, llm.ErrManagerClosed):
		writeAPIError(response, http.StatusServiceUnavailable, "manager_closed", err.Error())
	default:
		writeAPIError(response, http.StatusBadRequest, "invalid_run", err.Error())
	}
}
