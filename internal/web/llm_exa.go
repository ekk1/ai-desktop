package web

import (
	"errors"
	"net/http"

	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/llm"
	"github.com/ekk1/ai-desktop/internal/session"
)

type llmExaHandler struct {
	service *llm.ExaService
	maxBody int64
}

func (handler llmExaHandler) execute(response http.ResponseWriter, request *http.Request, sessionID, panelID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, true) {
		return
	}
	created, err := handler.service.Execute(request.Context(), sessionID, panelID)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, toPanelResponse(created))
}

func (handler llmExaHandler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrSessionNotFound), errors.Is(err, session.ErrPanelNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, llm.ErrNotExaCandidate):
		writeAPIError(response, http.StatusBadRequest, "not_exa_candidate", err.Error())
	case errors.Is(err, exa.ErrMissingAPIKey):
		writeAPIError(response, http.StatusBadRequest, "exa_not_configured", err.Error())
	default:
		writeAPIError(response, http.StatusBadGateway, "exa_failed", err.Error())
	}
}
