package session

import "time"

const workspaceSchemaVersion = 1

type Session struct {
	ID             string    `json:"id"`
	Title          string    `json:"title"`
	Folder         string    `json:"folder"`
	CurrentPanelID string    `json:"current_panel_id"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type Panel struct {
	ID           string          `json:"id"`
	ParentID     string          `json:"parent_id,omitempty"`
	Title        string          `json:"title"`
	Content      string          `json:"content"`
	Included     bool            `json:"included"`
	Collapsed    bool            `json:"collapsed"`
	Order        int             `json:"order"`
	KnowledgeIDs []string        `json:"knowledge_ids"`
	AssetIDs     []string        `json:"asset_ids"`
	Revisions    []Revision      `json:"revisions"`
	Result       *ResultMetadata `json:"result,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type Revision struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	Content      string    `json:"content"`
	Included     bool      `json:"included"`
	KnowledgeIDs []string  `json:"knowledge_ids"`
	AssetIDs     []string  `json:"asset_ids"`
	CreatedAt    time.Time `json:"created_at"`
}

type ResultMetadata struct {
	Source         string `json:"source,omitempty"`
	RunID          string `json:"run_id,omitempty"`
	QuickPathID    string `json:"quick_path_id,omitempty"`
	StopReason     string `json:"stop_reason,omitempty"`
	ErrorSummary   string `json:"error_summary,omitempty"`
	RequestSummary string `json:"request_summary,omitempty"`
}

type Workspace struct {
	SchemaVersion int     `json:"schema_version"`
	Session       Session `json:"session"`
	Panels        []Panel `json:"panels"`
}

type Filter struct {
	Query  string
	Folder string
}

type CreateSessionInput struct {
	Title  string `json:"title"`
	Folder string `json:"folder"`
}

type UpdateSessionInput struct {
	Title          string `json:"title"`
	Folder         string `json:"folder"`
	CurrentPanelID string `json:"current_panel_id"`
}

type CreatePanelInput struct {
	ParentID     string          `json:"parent_id"`
	Title        string          `json:"title"`
	Content      string          `json:"content"`
	Included     bool            `json:"included"`
	Collapsed    bool            `json:"collapsed"`
	KnowledgeIDs []string        `json:"knowledge_ids"`
	AssetIDs     []string        `json:"asset_ids"`
	Result       *ResultMetadata `json:"result,omitempty"`
}

type UpdatePanelInput struct {
	Title        string   `json:"title"`
	Content      string   `json:"content"`
	Included     bool     `json:"included"`
	Collapsed    bool     `json:"collapsed"`
	KnowledgeIDs []string `json:"knowledge_ids"`
	AssetIDs     []string `json:"asset_ids"`
}

type ForkSessionInput struct {
	PanelID string `json:"panel_id"`
	Title   string `json:"title"`
	Folder  string `json:"folder"`
}
