package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ekk1/ai-desktop/internal/knowledge"
)

type knowledgeHandler struct {
	service *knowledge.Service
	maxBody int64
}

func (handler knowledgeHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/knowledge"), "/")
	if path == "" {
		handler.collection(response, request)
		return
	}
	if strings.Contains(path, "/") {
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
		return
	}
	handler.item(response, request, path)
}

func (handler knowledgeHandler) collection(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items := handler.service.List(knowledge.Filter{Folder: request.URL.Query().Get("folder"), Query: request.URL.Query().Get("q")})
		writeJSON(response, http.StatusOK, struct {
			Notes []knowledge.Note `json:"notes"`
		}{Notes: items})
	case http.MethodPost:
		var input knowledge.Input
		if !handler.decode(response, request, &input) {
			return
		}
		created, err := handler.service.Create(input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, created)
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (handler knowledgeHandler) item(response http.ResponseWriter, request *http.Request, id string) {
	switch request.Method {
	case http.MethodGet:
		item, ok := handler.service.Get(id)
		if !ok {
			handler.writeError(response, knowledge.ErrNotFound)
			return
		}
		writeJSON(response, http.StatusOK, item)
	case http.MethodPut:
		var input knowledge.Input
		if !handler.decode(response, request, &input) {
			return
		}
		updated, err := handler.service.Update(id, input)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
	case http.MethodDelete:
		if err := handler.service.Delete(id); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPut+", "+http.MethodDelete)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (handler knowledgeHandler) decode(response http.ResponseWriter, request *http.Request, target any) bool {
	return decodeStrictJSON(response, request, handler.maxBody, target, false)
}

func (handler knowledgeHandler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, knowledge.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, knowledge.ErrAssetNotFound):
		writeAPIError(response, http.StatusBadRequest, "invalid_reference", err.Error())
	default:
		writeAPIError(response, http.StatusBadRequest, "invalid_note", fmt.Sprint(err))
	}
}
