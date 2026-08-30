package llm

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
)

var (
	ErrKnowledgeNotFound = errors.New("snapshot knowledge note not found")
	ErrAssetNotFound     = errors.New("snapshot asset not found")
	ErrUnsupportedAsset  = errors.New("LLM snapshot only supports image assets")
	ErrAssetLimit        = errors.New("LLM snapshot asset size limit exceeded")
)

type Assembler struct {
	knowledge *knowledge.Service
	assets    *asset.Repository
}

func NewAssembler(knowledgeService *knowledge.Service, assets *asset.Repository) *Assembler {
	return &Assembler{knowledge: knowledgeService, assets: assets}
}

func (assembler *Assembler) Build(
	workspace session.Workspace,
	panelID string,
	providerConfig provider.Provider,
	quickPath provider.QuickPath,
) (provider.PreparedRequest, Snapshot, error) {
	path, err := workspacePath(workspace.Panels, panelID)
	if err != nil {
		return provider.PreparedRequest{}, Snapshot{}, err
	}
	panels := make([]PanelSnapshot, 0, len(path))
	knowledgeItems := make([]KnowledgeSnapshot, 0)
	contentParts := make([]string, 0)
	assetIDs := make([]string, 0)
	seenAssets := make(map[string]struct{})
	appendAssetID := func(assetID string) {
		if assetID == "" {
			return
		}
		if _, duplicate := seenAssets[assetID]; duplicate {
			return
		}
		seenAssets[assetID] = struct{}{}
		assetIDs = append(assetIDs, assetID)
	}

	for _, panel := range path {
		if !panel.Included {
			continue
		}
		panelSnapshot := PanelSnapshot{
			ID: panel.ID, ParentID: panel.ParentID, Title: panel.Title, Content: panel.Content, Included: true,
			KnowledgeIDs: cloneStrings(panel.KnowledgeIDs), AssetIDs: cloneStrings(panel.AssetIDs),
		}
		panels = append(panels, panelSnapshot)
		appendContent(&contentParts, panel.Content)
		for _, assetID := range panel.AssetIDs {
			appendAssetID(assetID)
		}
		for _, knowledgeID := range panel.KnowledgeIDs {
			note, exists := assembler.knowledge.Get(knowledgeID)
			if !exists {
				return provider.PreparedRequest{}, Snapshot{}, fmt.Errorf("%w: %s", ErrKnowledgeNotFound, knowledgeID)
			}
			knowledgeSnapshot := KnowledgeSnapshot{
				ID: note.ID, Folder: note.Folder, Title: note.Title, Content: note.Content,
				Tags: cloneStrings(note.Tags), AssetIDs: cloneStrings(note.AssetIDs),
			}
			knowledgeItems = append(knowledgeItems, knowledgeSnapshot)
			appendContent(&contentParts, joinKnowledgeContent(note.Title, note.Content))
			for _, assetID := range note.AssetIDs {
				appendAssetID(assetID)
			}
		}
	}

	assetSnapshots, dataURLs, err := assembler.encodeAssets(assetIDs, providerConfig.MaxAssetBytes)
	if err != nil {
		return provider.PreparedRequest{}, Snapshot{}, err
	}
	content := strings.Join(contentParts, "\n\n")
	prepared, err := provider.Render(providerConfig, quickPath, provider.TemplateVariables{
		Content: content, Panels: panels, Knowledge: knowledgeItems, AssetDataURLs: dataURLs,
	})
	if err != nil {
		return provider.PreparedRequest{}, Snapshot{}, err
	}
	snapshotProvider := cloneProviderForSnapshot(providerConfig, prepared.SnapshotHeaders)
	snapshot := Snapshot{
		Session: workspace.Session, PanelID: panelID, Panels: panels, Knowledge: knowledgeItems,
		Assets: assetSnapshots, Content: content, AssetDataURLs: cloneStrings(dataURLs),
		QuickPath: cloneQuickPath(quickPath), Provider: snapshotProvider,
		URL: prepared.URL, Headers: cloneStringMap(prepared.SnapshotHeaders), Body: append([]byte{}, prepared.Body...),
		CreatedAt: time.Now().UTC(),
	}
	return prepared, snapshot, nil
}

func (assembler *Assembler) encodeAssets(assetIDs []string, limit int64) ([]AssetSnapshot, []string, error) {
	assetSnapshots := make([]AssetSnapshot, 0, len(assetIDs))
	dataURLs := make([]string, 0, len(assetIDs))
	var total int64
	for _, assetID := range assetIDs {
		item, exists := assembler.assets.Get(assetID)
		if !exists {
			return nil, nil, fmt.Errorf("%w: %s", ErrAssetNotFound, assetID)
		}
		if !strings.HasPrefix(item.MediaType, "image/") {
			return nil, nil, fmt.Errorf("%w: %s has media type %s", ErrUnsupportedAsset, assetID, item.MediaType)
		}
		if limit < 1 || item.Size < 0 || item.Size > limit-total {
			return nil, nil, fmt.Errorf("%w: limit %d bytes", ErrAssetLimit, limit)
		}
		file, current, err := assembler.assets.OpenContent(assetID)
		if err != nil {
			return nil, nil, fmt.Errorf("open snapshot asset %q: %w", assetID, err)
		}
		remaining := limit - total
		contents, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
		closeErr := file.Close()
		if readErr != nil {
			return nil, nil, fmt.Errorf("read snapshot asset %q: %w", assetID, readErr)
		}
		if closeErr != nil {
			return nil, nil, fmt.Errorf("close snapshot asset %q: %w", assetID, closeErr)
		}
		if int64(len(contents)) > remaining {
			return nil, nil, fmt.Errorf("%w: limit %d bytes", ErrAssetLimit, limit)
		}
		total += int64(len(contents))
		assetSnapshots = append(assetSnapshots, AssetSnapshot{
			ID: current.ID, SHA256: current.SHA256, MediaType: current.MediaType, DisplayName: current.DisplayName,
			Size: current.Size, Width: current.Width, Height: current.Height, State: current.State, Source: current.Source,
		})
		dataURLs = append(dataURLs, "data:"+current.MediaType+";base64,"+base64.StdEncoding.EncodeToString(contents))
	}
	return assetSnapshots, dataURLs, nil
}

func workspacePath(panels []session.Panel, panelID string) ([]session.Panel, error) {
	byID := make(map[string]session.Panel, len(panels))
	for _, panel := range panels {
		byID[panel.ID] = panel
	}
	cursor, exists := byID[panelID]
	if !exists {
		return nil, session.ErrPanelNotFound
	}
	reversed := make([]session.Panel, 0)
	seen := make(map[string]struct{})
	for {
		if _, duplicate := seen[cursor.ID]; duplicate {
			return nil, fmt.Errorf("panel path contains a cycle")
		}
		seen[cursor.ID] = struct{}{}
		reversed = append(reversed, cursor)
		if cursor.ParentID == "" {
			break
		}
		parent, exists := byID[cursor.ParentID]
		if !exists {
			return nil, fmt.Errorf("panel %q has a missing parent", cursor.ID)
		}
		cursor = parent
	}
	path := make([]session.Panel, len(reversed))
	for index := range reversed {
		path[len(reversed)-1-index] = reversed[index]
	}
	return path, nil
}

func appendContent(parts *[]string, content string) {
	if content != "" {
		*parts = append(*parts, content)
	}
}

func joinKnowledgeContent(title, content string) string {
	if content == "" {
		return title
	}
	return title + "\n" + content
}

func cloneProviderForSnapshot(source provider.Provider, headers map[string]string) provider.Provider {
	clone := source
	clone.APIKey = ""
	clone.Headers = cloneStringMap(headers)
	return clone
}

func cloneQuickPath(source provider.QuickPath) provider.QuickPath {
	clone := source
	clone.Params = append([]byte{}, source.Params...)
	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func cloneStrings(source []string) []string {
	if source == nil {
		return []string{}
	}
	return append([]string{}, source...)
}
