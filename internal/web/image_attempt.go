package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/imagegen"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

type imageAttemptHandler struct {
	manager   *imagegen.Manager
	maxBody   int64
	heartbeat time.Duration
}

func (handler imageAttemptHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/images/attempts"), "/")
	segments := strings.Split(path, "/")
	if len(segments) == 0 || !validLLMID(segments[0]) {
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	switch {
	case len(segments) == 1:
		handler.get(response, request, segments[0])
	case len(segments) == 2 && segments[1] == "cancel":
		handler.cancel(response, request, segments[0])
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler imageAttemptHandler) executeBatch(response http.ResponseWriter, request *http.Request, batchID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	attempts, err := handler.manager.StartBatch(batchID)
	if err != nil {
		writeImageAPIError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, struct {
		Attempts []imagegen.Attempt `json:"attempts"`
	}{Attempts: attempts})
}

func (handler imageAttemptHandler) executeItem(response http.ResponseWriter, request *http.Request, batchID, itemID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	attempt, err := handler.manager.StartItem(batchID, itemID)
	if err != nil && attempt.ID == "" {
		writeImageAPIError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, struct {
		Attempts []imagegen.Attempt `json:"attempts"`
	}{Attempts: []imagegen.Attempt{attempt}})
}

func (handler imageAttemptHandler) get(response http.ResponseWriter, request *http.Request, attemptID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	attempt, ok := handler.manager.GetAttempt(attemptID)
	if !ok {
		writeImageAPIError(response, imagegen.ErrAttemptNotFound)
		return
	}
	writeJSON(response, http.StatusOK, attempt)
}

func (handler imageAttemptHandler) cancel(response http.ResponseWriter, request *http.Request, attemptID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	before, ok := handler.manager.GetAttempt(attemptID)
	if !ok {
		writeImageAPIError(response, imagegen.ErrAttemptNotFound)
		return
	}
	if terminalImageAttempt(before.State) {
		writeJSON(response, http.StatusOK, before)
		return
	}
	if err := handler.manager.Cancel(attemptID); err != nil {
		writeImageCancelError(response, err)
		return
	}
	after, _ := handler.manager.GetAttempt(attemptID)
	writeJSON(response, http.StatusAccepted, after)
}

func (handler imageAttemptHandler) events(response http.ResponseWriter, request *http.Request, batchID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(response, http.MethodGet)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		writeAPIError(response, http.StatusInternalServerError, "stream_unavailable", "streaming is unavailable")
		return
	}
	events, unsubscribe, err := handler.manager.SubscribeBatch(batchID)
	if err != nil {
		writeImageAPIError(response, err)
		return
	}
	defer unsubscribe()
	response.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	response.Header().Set("X-Accel-Buffering", "no")
	response.WriteHeader(http.StatusOK)
	flusher.Flush()
	interval := handler.heartbeat
	if interval <= 0 {
		interval = 15 * time.Second
	}
	heartbeat := time.NewTicker(interval)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-events:
			if !open || writeImageAttemptEvent(response, event) != nil {
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

func writeImageAttemptEvent(response http.ResponseWriter, event imagegen.AttemptEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(response, "event: %s\ndata: %s\n\n", event.Type, encoded)
	return err
}

func terminalImageAttempt(state imagegen.AttemptState) bool {
	return state == imagegen.AttemptSucceeded || state == imagegen.AttemptFailed || state == imagegen.AttemptCancelled || state == imagegen.AttemptInterrupted
}

func writeImageCancelError(response http.ResponseWriter, err error) {
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		writeImageAPIError(response, err)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		writeAPIError(response, http.StatusGatewayTimeout, "provider_timeout", "image provider cancellation timed out")
		return
	}
	var upstream *sdcpp.HTTPError
	if errors.As(err, &upstream) {
		writeAPIError(response, http.StatusBadGateway, "provider_error", upstream.Error())
		return
	}
	if errors.Is(err, imagegen.ErrAttemptNotFound) || errors.Is(err, imagegen.ErrImageManagerClosed) {
		writeImageAPIError(response, err)
		return
	}
	writeAPIError(response, http.StatusBadGateway, "provider_error", "image provider cancellation failed")
}
