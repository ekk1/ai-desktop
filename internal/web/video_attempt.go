package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/videogen"
)

type videoAttemptHandler struct {
	manager   *videogen.Manager
	maxBody   int64
	heartbeat time.Duration
	dataDir   string
}

func (h videoAttemptHandler) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/videos/attempts"), "/")
	s := strings.Split(p, "/")
	if len(s) == 0 || !validVideoID(s[0]) {
		writeAPIError(w, 404, "not_found", "resource not found")
		return
	}
	switch {
	case len(s) == 1:
		h.get(w, r, s[0])
	case len(s) == 2 && s[1] == "cancel":
		h.cancel(w, r, s[0])
	case len(s) == 2 && s[1] == "logs":
		h.logs(w, r, s[0])
	case len(s) == 3 && s[1] == "logs" && s[2] == "save":
		h.saveLog(w, r, s[0])
	case len(s) == 2 && s[1] == "workspace":
		h.cleanup(w, r, s[0])
	case len(s) == 2 && s[1] == "retry":
		h.retry(w, r, s[0])
	default:
		writeAPIError(w, 404, "not_found", "resource not found")
	}
}
func (h videoAttemptHandler) executeBatch(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	out, err := h.manager.StartBatch(id)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	writeJSON(w, 202, struct {
		Attempts []videogen.Attempt `json:"attempts"`
	}{out})
}
func (h videoAttemptHandler) executeItem(w http.ResponseWriter, r *http.Request, batch, item string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	out, err := h.manager.StartItem(batch, item)
	if err != nil && out.ID == "" {
		writeVideoAPIError(w, err)
		return
	}
	writeJSON(w, 202, struct {
		Attempts []videogen.Attempt `json:"attempts"`
	}{[]videogen.Attempt{out}})
}
func (h videoAttemptHandler) get(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	out, ok := h.manager.GetAttempt(id)
	if !ok {
		writeVideoAPIError(w, videogen.ErrAttemptNotFound)
		return
	}
	writeJSON(w, 200, out)
}
func (h videoAttemptHandler) retry(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	out, err := h.manager.Retry(id)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	writeJSON(w, 202, out)
}
func (h videoAttemptHandler) cancel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	before, ok := h.manager.GetAttempt(id)
	if !ok {
		writeVideoAPIError(w, videogen.ErrAttemptNotFound)
		return
	}
	if terminalVideoAttempt(before.State) {
		writeJSON(w, 200, before)
		return
	}
	if err := h.manager.Cancel(id); err != nil {
		if err == context.DeadlineExceeded {
			writeAPIError(w, 504, "provider_timeout", "video provider cancellation timed out")
		} else {
			writeVideoAPIError(w, err)
		}
		return
	}
	after, _ := h.manager.GetAttempt(id)
	writeJSON(w, 202, after)
}
func (h videoAttemptHandler) events(w http.ResponseWriter, r *http.Request, batch string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, 500, "stream_unavailable", "streaming is unavailable")
		return
	}
	events, unsub, err := h.manager.SubscribeBatch(batch)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	flush.Flush()
	d := h.heartbeat
	if d <= 0 {
		d = 15 * time.Second
	}
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case e, ok := <-events:
			if !ok || writeVideoEvent(w, e) != nil {
				return
			}
			flush.Flush()
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flush.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
func (h videoAttemptHandler) logs(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, 500, "stream_unavailable", "streaming is unavailable")
		return
	}
	snapshot, ch, unsub, err := h.manager.SubscribeCLILog(id)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	if writeVideoLogSnapshot(w, snapshot) != nil {
		return
	}
	flush.Flush()
	interval := h.heartbeat
	if interval <= 0 {
		interval = 15 * time.Second
	}
	heartbeat := time.NewTicker(interval)
	defer heartbeat.Stop()
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				return
			}
			if writeVideoLogChunk(w, c.Offset, c.Data) != nil {
				return
			}
			flush.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flush.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
func (h videoAttemptHandler) saveLog(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	path, err := h.manager.SaveCLILog(id)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	location, err := safeDataLocation(h.dataDir, path)
	if err != nil {
		writeAPIError(w, 500, "storage_error", "video log could not be saved safely")
		return
	}
	writeJSON(w, 200, struct {
		WorkspacePath string `json:"workspace_path"`
	}{location})
}
func (h videoAttemptHandler) cleanup(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w, "DELETE")
		return
	}
	if err := h.manager.CleanupWorkspace(id); err != nil {
		writeVideoAPIError(w, err)
		return
	}
	w.WriteHeader(204)
}
func terminalVideoAttempt(s videogen.AttemptState) bool {
	return s == videogen.AttemptSucceeded || s == videogen.AttemptFailed || s == videogen.AttemptCancelled || s == videogen.AttemptInterrupted
}
func writeVideoEvent(w http.ResponseWriter, e videogen.AttemptEvent) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, b)
	return err
}
func writeVideoLogSnapshot(w http.ResponseWriter, s videogen.VideoLogSnapshot) error {
	b, err := json.Marshal(struct {
		StartOffset int64  `json:"start_offset"`
		EndOffset   int64  `json:"end_offset"`
		DataBase64  string `json:"data_base64"`
	}{s.StartOffset, s.EndOffset, base64.StdEncoding.EncodeToString(s.Data)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: snapshot\ndata: %s\n\n", b)
	return err
}
func writeVideoLogChunk(w http.ResponseWriter, o int64, d []byte) error {
	b, err := json.Marshal(struct {
		StartOffset int64  `json:"start_offset"`
		DataBase64  string `json:"data_base64"`
	}{o, base64.StdEncoding.EncodeToString(d)})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: chunk\ndata: %s\n\n", b)
	return err
}
