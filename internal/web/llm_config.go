package web

import (
	"net/http"
	"strings"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/provider"
)

type llmConfigHandler struct {
	repository *config.Repository
	maxBody    int64
}

func (handler llmConfigHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/llm"), "/")
	switch path {
	case "config":
		handler.config(response, request)
	case "providers/preset/llama-completion":
		handler.llamaPreset(response, request)
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler llmConfigHandler) config(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		writeJSON(response, http.StatusOK, handler.repository.Snapshot().LLM)
	case http.MethodPut:
		var configuration provider.LLMConfig
		if !decodeStrictJSON(response, request, handler.maxBody, &configuration, false) {
			return
		}
		updated, err := handler.repository.UpdateLLM(configuration)
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, "invalid_llm_config", err.Error())
			return
		}
		writeJSON(response, http.StatusOK, updated.LLM)
	default:
		methodNotAllowed(response, http.MethodGet+", "+http.MethodPut)
	}
}

func (handler llmConfigHandler) llamaPreset(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		methodNotAllowed(response, http.MethodPost)
		return
	}
	var input struct{}
	if !decodeStrictJSON(response, request, handler.maxBody, &input, true) {
		return
	}
	configuration := handler.repository.Snapshot().LLM
	if err := configuration.AddLlamaCompletionPreset(); err != nil {
		status := http.StatusBadRequest
		code := "invalid_llm_config"
		if strings.Contains(err.Error(), "already exists") {
			status = http.StatusConflict
			code = "conflict"
		}
		writeAPIError(response, status, code, err.Error())
		return
	}
	updated, err := handler.repository.UpdateLLM(configuration)
	if err != nil {
		writeAPIError(response, http.StatusBadRequest, "invalid_llm_config", err.Error())
		return
	}
	writeJSON(response, http.StatusCreated, updated.LLM)
}
