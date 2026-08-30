package llm

import (
	"encoding/json"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
)

type PanelSnapshot struct {
	ID           string   `json:"id"`
	ParentID     string   `json:"parent_id,omitempty"`
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Included     bool     `json:"included"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	AssetIDs     []string `json:"asset_ids"`
}

type KnowledgeSnapshot struct {
	ID       string   `json:"id"`
	Folder   string   `json:"folder,omitempty"`
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Tags     []string `json:"tags"`
	AssetIDs []string `json:"asset_ids"`
}

type AssetSnapshot struct {
	ID          string      `json:"id"`
	SHA256      string      `json:"sha256"`
	MediaType   string      `json:"media_type"`
	DisplayName string      `json:"display_name"`
	Size        int64       `json:"size"`
	Width       int         `json:"width,omitempty"`
	Height      int         `json:"height,omitempty"`
	State       asset.State `json:"state"`
	Source      string      `json:"source,omitempty"`
}

type Snapshot struct {
	Session       session.Session     `json:"session"`
	PanelID       string              `json:"panel_id"`
	Panels        []PanelSnapshot     `json:"panels"`
	Knowledge     []KnowledgeSnapshot `json:"knowledge"`
	Assets        []AssetSnapshot     `json:"assets"`
	Content       string              `json:"content"`
	AssetDataURLs []string            `json:"asset_data_urls"`
	QuickPath     provider.QuickPath  `json:"quick_path"`
	Provider      provider.Provider   `json:"provider"`
	URL           string              `json:"url"`
	Headers       map[string]string   `json:"headers"`
	Body          json.RawMessage     `json:"body"`
	CreatedAt     time.Time           `json:"created_at"`
}
