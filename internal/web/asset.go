package web

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/ekk1/ai-desktop/internal/asset"
)

type assetHandler struct {
	repository *asset.Repository
	maxBody    int64
}

func (handler assetHandler) serve(response http.ResponseWriter, request *http.Request) {
	path := strings.Trim(strings.TrimPrefix(request.URL.Path, "/api/v1/assets"), "/")
	if path == "" {
		handler.collection(response, request)
		return
	}
	if path == "state" {
		handler.batchState(response, request)
		return
	}
	if path == "export" {
		handler.export(response, request)
		return
	}
	segments := strings.Split(path, "/")
	id := segments[0]
	if len(segments) == 1 {
		handler.item(response, request, id)
		return
	}
	switch strings.Join(segments[1:], "/") {
	case "content":
		handler.content(response, request, id)
	case "state":
		handler.state(response, request, id)
	default:
		writeAPIError(response, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (handler assetHandler) batchState(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var input struct {
		AssetIDs []string    `json:"asset_ids"`
		State    asset.State `json:"state"`
	}
	if !handler.decode(response, request, &input) {
		return
	}
	items, err := handler.repository.SetStates(input.AssetIDs, input.State)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Assets []asset.Asset `json:"assets"`
	}{Assets: items})
}

func (handler assetHandler) export(response http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var input struct {
		AssetIDs []string `json:"asset_ids"`
	}
	if !handler.decode(response, request, &input) {
		return
	}
	if len(input.AssetIDs) == 0 {
		writeAPIError(response, http.StatusBadRequest, "invalid_asset", "at least one asset ID is required")
		return
	}

	type selectedContent struct {
		file io.ReadCloser
		item asset.Asset
	}
	selected := make([]selectedContent, 0, len(input.AssetIDs))
	seen := make(map[string]struct{}, len(input.AssetIDs))
	defer func() {
		for _, content := range selected {
			_ = content.file.Close()
		}
	}()
	for _, id := range input.AssetIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		file, item, err := handler.repository.OpenContent(id)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		selected = append(selected, selectedContent{file: file, item: item})
	}

	response.Header().Set("Content-Type", "application/zip")
	response.Header().Set("Content-Disposition", `attachment; filename="assets.zip"`)
	writer := zip.NewWriter(response)
	names := make(map[string]int, len(selected))
	for _, content := range selected {
		name := uniqueArchiveName(content.item.DisplayName, names)
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			return
		}
		if _, err := io.Copy(entry, content.file); err != nil {
			return
		}
	}
	_ = writer.Close()
}

func uniqueArchiveName(displayName string, used map[string]int) string {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(displayName), `\`, "/"))
	if name == "" || name == "." {
		name = "asset"
	}
	name = strings.Map(func(character rune) rune {
		if character < 0x20 || character == 0x7f {
			return '_'
		}
		return character
	}, name)
	count := used[name]
	used[name] = count + 1
	if count == 0 {
		return name
	}
	extension := path.Ext(name)
	base := strings.TrimSuffix(name, extension)
	for {
		count++
		candidate := base + "-" + strconv.Itoa(count) + extension
		if used[candidate] == 0 {
			used[candidate] = 1
			return candidate
		}
	}
}

func (handler assetHandler) collection(response http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		items := handler.repository.List(asset.Filter{
			State: asset.State(request.URL.Query().Get("state")), MediaType: request.URL.Query().Get("media_type"), Query: request.URL.Query().Get("q"),
		})
		writeJSON(response, http.StatusOK, struct {
			Assets []asset.Asset `json:"assets"`
		}{Assets: items})
	case http.MethodPost:
		request.Body = http.MaxBytesReader(response, request.Body, handler.maxBody)
		if err := request.ParseMultipartForm(handler.maxBody); err != nil {
			writeAPIError(response, http.StatusBadRequest, "invalid_upload", "upload must be valid multipart form data")
			return
		}
		file, header, err := request.FormFile("file")
		if err != nil {
			writeAPIError(response, http.StatusBadRequest, "missing_file", "multipart field file is required")
			return
		}
		defer file.Close()
		mediaType := request.FormValue("media_type")
		if mediaType == "" {
			mediaType = header.Header.Get("Content-Type")
		}
		created, err := handler.repository.Import(asset.ImportInput{Reader: file, DisplayName: header.Filename, MediaType: mediaType, Source: request.FormValue("source")})
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

func (handler assetHandler) item(response http.ResponseWriter, request *http.Request, id string) {
	switch request.Method {
	case http.MethodGet:
		item, ok := handler.repository.Get(id)
		if !ok {
			handler.writeError(response, asset.ErrNotFound)
			return
		}
		writeJSON(response, http.StatusOK, item)
	case http.MethodPatch:
		var input struct {
			DisplayName string `json:"display_name"`
			Notes       string `json:"notes"`
		}
		if !handler.decode(response, request, &input) {
			return
		}
		updated, err := handler.repository.UpdateMetadata(id, input.DisplayName, input.Notes)
		if err != nil {
			handler.writeError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, updated)
	case http.MethodDelete:
		if err := handler.repository.Delete(id); err != nil {
			handler.writeError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	default:
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodPatch+", "+http.MethodDelete)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (handler assetHandler) state(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	var input struct {
		State asset.State `json:"state"`
	}
	if !handler.decode(response, request, &input) {
		return
	}
	updated, err := handler.repository.SetState(id, input.State)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, updated)
}

func (handler assetHandler) content(response http.ResponseWriter, request *http.Request, id string) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		writeAPIError(response, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	file, item, err := handler.repository.OpenContent(id)
	if err != nil {
		handler.writeError(response, err)
		return
	}
	defer file.Close()
	response.Header().Set("Content-Type", item.MediaType)
	response.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	disposition := "attachment"
	if assetMayRenderInline(item.MediaType) {
		disposition = "inline"
	}
	response.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": item.DisplayName}))
	http.ServeContent(response, request, item.DisplayName, item.UpdatedAt, file)
}

func assetMayRenderInline(mediaType string) bool {
	parsed, _, err := mime.ParseMediaType(mediaType)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed) {
	case "image/png", "image/jpeg", "image/gif", "image/webp",
		"video/mp4", "video/webm", "video/quicktime", "video/x-msvideo", "video/avi",
		"text/plain":
		return true
	default:
		return false
	}
}

func (handler assetHandler) decode(response http.ResponseWriter, request *http.Request, target any) bool {
	return decodeStrictJSON(response, request, handler.maxBody, target, false)
}

func (handler assetHandler) writeError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, asset.ErrNotFound):
		writeAPIError(response, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, asset.ErrReferenced):
		writeAPIError(response, http.StatusConflict, "asset_referenced", err.Error())
	default:
		writeAPIError(response, http.StatusBadRequest, "invalid_asset", fmt.Sprint(err))
	}
}
