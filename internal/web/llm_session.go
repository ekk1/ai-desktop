package web

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/session"
)

type panelResponse struct {
	session.Panel
	ExaCandidate bool `json:"exa_candidate"`
}

type branchOption struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type branchResponse struct {
	ParentID        string         `json:"parent_id"`
	SelectedChildID string         `json:"selected_child_id,omitempty"`
	Options         []branchOption `json:"options"`
}

type sessionWorkspaceResponse struct {
	Session     session.Session  `json:"session"`
	Panels      []panelResponse  `json:"panels"`
	CurrentPath []panelResponse  `json:"current_path"`
	Branches    []branchResponse `json:"branches"`
}

type llmSessionHandler struct {
	service *session.Service
	maxBody int64
	runs    *llmRunHandler
	exa     *llmExaHandler
}

func (handler llmSessionHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/llm/sessions"), "/")
	if path == "" {
		handler.collection(response, request)
		return
	}
	segments := strings.Split(path, "/")
	if len(segments) == 0 || !validLLMID(segments[0]) {
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	sessionID := segments[0]
	switch {
	case len(segments) == 1:
		handler.item(response, request, sessionID)
	case len(segments) == 2 && segments[1] == "fork":
		handler.fork(response, request, sessionID)
	case len(segments) == 2 && segments[1] == "panels":
		handler.createPanel(response, request, sessionID)
	case len(segments) == 2 && segments[1] == "execute" && handler.runs != nil:
		handler.runs.execute(response, request, sessionID)
	case len(segments) == 3 && segments[1] == "panels" && validLLMID(segments[2]):
		handler.panel(response, request, sessionID, segments[2])
	case len(segments) == 5 && segments[1] == "panels" && validLLMID(segments[2]) && segments[3] == "restore" && validLLMID(segments[4]):
		handler.restore(response, request, sessionID, segments[2], segments[4])
	case len(segments) == 4 && segments[1] == "panels" && validLLMID(segments[2]) && segments[3] == "exa" && handler.exa != nil:
		handler.exa.execute(response, request, sessionID, segments[2])
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler llmSessionHandler) collection(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, struct {
			Sessions []session.Session `json:"sessions"`
		}{Sessions: handler.service.List(session.Filter{Folder: request.URL.Query().Get("folder"), Query: request.URL.Query().Get("q")})})
	case http.MethodPost:
		var input session.CreateSessionInput
		if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
			return
		}
		workspace, err := handler.service.CreateSession(input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		handler.writeWorkspace(response, http.StatusCreated, workspace)
	default:
		methodNotAllowed(response, http.MethodGet+", "+http.MethodPost)
	}
}

func (handler llmSessionHandler) item(response http.ResponseWriter, request *http.Request, sessionID string) {
	switch request.Method {
	case http.MethodGet:
		workspace, exists := handler.service.Get(sessionID)
		if !exists {
			handler.writeError(response, session.ErrSessionNotFound)
			return
		}
		handler.writeWorkspace(response, http.StatusOK, workspace)
	case http.MethodPut:
		var input session.UpdateSessionInput
		if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
			return
		}
		workspace, err := handler.service.UpdateSession(sessionID, input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		handler.writeWorkspace(response, http.StatusOK, workspace)
	case http.MethodDelete:
		if err := handler.service.DeleteSession(sessionID); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response, http.MethodGet+", "+http.MethodPut+", "+http.MethodDelete)
	}
}

func (handler llmSessionHandler) fork(response http.ResponseWriter, request *http.Request, sessionID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input session.ForkSessionInput
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	workspace, err := handler.service.ForkSession(sessionID, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	handler.writeWorkspace(response, http.StatusCreated, workspace)
}

func (handler llmSessionHandler) createPanel(response http.ResponseWriter, request *http.Request, sessionID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input session.CreatePanelInput
	if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
		return
	}
	created, err := handler.service.CreatePanel(sessionID, input)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, toPanelResponse(created))
}

func (handler llmSessionHandler) panel(response http.ResponseWriter, request *http.Request, sessionID, panelID string) {
	switch request.Method {
	case http.MethodPut:
		var input session.UpdatePanelInput
		if !decodeStrictJSON(response, request, handler.maxBody, &input, false) {
			return
		}
		updated, err := handler.service.UpdatePanel(sessionID, panelID, input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, toPanelResponse(updated))
	case http.MethodDelete:
		if err := handler.service.DeletePanel(sessionID, panelID); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		methodNotAllowed(response, http.MethodPut+", "+http.MethodDelete)
	}
}

func (handler llmSessionHandler) restore(response http.ResponseWriter, request *http.Request, sessionID, panelID, revisionID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, true) {
		return
	}
	restored, err := handler.service.RestoreRevision(sessionID, panelID, revisionID)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, toPanelResponse(restored))
}

func (handler llmSessionHandler) writeWorkspace(response http.ResponseWriter, status int, workspace session.Workspace) {
	path, err := handler.service.PathTo(workspace.Session.ID, workspace.Session.CurrentPanelID)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, status, buildWorkspaceResponse(workspace, path))
}

func buildWorkspaceResponse(workspace session.Workspace, path []session.Panel) sessionWorkspaceResponse {
	panels := make([]panelResponse, len(workspace.Panels))
	for index, panel := range workspace.Panels {
		panels[index] = toPanelResponse(panel)
	}
	currentPath := make([]panelResponse, len(path))
	for index, panel := range path {
		currentPath[index] = toPanelResponse(panel)
	}
	branches := make([]branchResponse, 0)
	for pathIndex, parent := range path {
		options := make([]branchOption, 0)
		for _, candidate := range workspace.Panels {
			if candidate.ParentID == parent.ID {
				options = append(options, branchOption{ID: candidate.ID, Title: candidate.Title})
			}
		}
		if len(options) == 0 {
			continue
		}
		selected := ""
		if pathIndex+1 < len(path) {
			selected = path[pathIndex+1].ID
		}
		branches = append(branches, branchResponse{ParentID: parent.ID, SelectedChildID: selected, Options: options})
	}
	return sessionWorkspaceResponse{Session: workspace.Session, Panels: panels, CurrentPath: currentPath, Branches: branches}
}

func toPanelResponse(panel session.Panel) panelResponse {
	_, candidate := exa.Detect(panel.Content)
	return panelResponse{Panel: panel, ExaCandidate: candidate}
}

func (handler llmSessionHandler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrSessionNotFound), errors.Is(err, session.ErrPanelNotFound), errors.Is(err, session.ErrRevisionNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, session.ErrRootPanel):
		writeAPIError(response, http.StatusConflict, "root_panel", err.Error())
	case errors.Is(err, session.ErrAssetNotFound):
		writeAPIError(response, http.StatusBadRequest, "invalid_reference", err.Error())
	default:
		writeAPIError(response, http.StatusBadRequest, "invalid_session", err.Error())
	}
}

func validLLMID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
