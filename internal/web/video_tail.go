package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/videogen"
)

type videoTailHandler struct {
	extractor  *videogen.TailExtractor
	repository *videogen.TailRepository
	maxBody    int64
	heartbeat  time.Duration
}

func (h videoTailHandler) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/videos/tail-extractions"), "/")
	if p == "" {
		h.collection(w, r)
		return
	}
	s := strings.Split(p, "/")
	if !validVideoID(s[0]) {
		writeAPIError(w, 404, "not_found", "resource not found")
		return
	}
	switch {
	case len(s) == 1:
		h.get(w, r, s[0])
	case len(s) == 2 && s[1] == "cancel":
		h.cancel(w, r, s[0])
	case len(s) == 2 && s[1] == "events":
		h.events(w, r, s[0])
	case len(s) == 3 && s[1] == "logs" && s[2] == "save":
		h.saveLog(w, r, s[0])
	default:
		writeAPIError(w, 404, "not_found", "resource not found")
	}
}
func (h videoTailHandler) collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, 200, struct {
			Extractions []videogen.TailExtraction `json:"extractions"`
		}{h.repository.List()})
	case http.MethodPost:
		var in struct {
			SourceAssetID string `json:"source_asset_id"`
			PresetID      string `json:"preset_id"`
		}
		if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
			return
		}
		if !validVideoID(in.SourceAssetID) {
			writeAPIError(w, 400, "invalid_video", "source asset ID is invalid")
			return
		}
		out, err := h.extractor.Extract(r.Context(), in.SourceAssetID, in.PresetID)
		if err != nil {
			writeVideoAPIError(w, err)
			return
		}
		writeJSON(w, 202, out)
	default:
		methodNotAllowed(w, "GET, POST")
	}
}
func (h videoTailHandler) get(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	out, ok := h.repository.Get(id)
	if !ok {
		writeVideoAPIError(w, videogen.ErrTailExtractionNotFound)
		return
	}
	writeJSON(w, 200, out)
}
func (h videoTailHandler) cancel(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	before, ok := h.repository.Get(id)
	if !ok {
		writeVideoAPIError(w, videogen.ErrTailExtractionNotFound)
		return
	}
	if terminalVideoAttempt(before.State) {
		writeJSON(w, 200, before)
		return
	}
	if err := h.extractor.CancelExtraction(r.Context(), id); err != nil {
		writeVideoAPIError(w, err)
		return
	}
	after, _ := h.repository.Get(id)
	writeJSON(w, 202, after)
}
func (h videoTailHandler) events(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	flush, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, 500, "stream_unavailable", "streaming is unavailable")
		return
	}
	snapshot, events, unsub, err := h.extractor.SubscribeExtraction(id)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	defer unsub()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(200)
	if writeTailEvent(w, "snapshot", snapshot) != nil {
		return
	}
	flush.Flush()
	d := h.heartbeat
	if d <= 0 {
		d = 15 * time.Second
	}
	ticker := time.NewTicker(d)
	defer ticker.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok || writeTailEvent(w, "state", event) != nil {
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
func (h videoTailHandler) saveLog(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	var in struct{}
	if !decodeStrictJSON(w, r, h.maxBody, &in, false) {
		return
	}
	path, err := h.extractor.SaveExtractionLog(id)
	if err != nil {
		writeVideoAPIError(w, err)
		return
	}
	writeJSON(w, 200, struct {
		WorkspacePath string `json:"workspace_path"`
	}{path})
}
func writeTailEvent(w http.ResponseWriter, typ string, e videogen.TailExtraction) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", typ, b)
	return err
}
