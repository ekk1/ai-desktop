package imagegen

import (
	"encoding/json"
	"time"

	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

const batchSchemaVersion = 1

type InputAssets struct {
	InitImageID      string   `json:"init_image_id,omitempty"`
	RefImageIDs      []string `json:"ref_image_ids"`
	MaskImageID      string   `json:"mask_image_id,omitempty"`
	ControlImageID   string   `json:"control_image_id,omitempty"`
	IPAdapterImageID string   `json:"ip_adapter_image_id,omitempty"`
}

type Batch struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Folder      string          `json:"folder"`
	ProviderID  string          `json:"provider_id"`
	Concurrency int             `json:"concurrency"`
	BaseParams  json.RawMessage `json:"base_params"`
	Items       []Item          `json:"items"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type Item struct {
	ID             string          `json:"id"`
	Order          int             `json:"order"`
	Prompt         string          `json:"prompt"`
	NegativePrompt string          `json:"negative_prompt"`
	ParamsOverride json.RawMessage `json:"params_override"`
	InputAssets    InputAssets     `json:"input_assets"`
	Attempts       []Attempt       `json:"attempts"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type Snapshot struct {
	Provider       sdcpp.ImageProvider `json:"provider"`
	Params         json.RawMessage     `json:"params"`
	Prompt         string              `json:"prompt"`
	NegativePrompt string              `json:"negative_prompt"`
	InputAssets    []AssetSnapshot     `json:"input_assets"`
	CreatedAt      time.Time           `json:"created_at"`
}

type AssetSnapshot struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"media_type"`
	DisplayName string `json:"display_name"`
	Size        int64  `json:"size"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

type Attempt struct {
	ID             string       `json:"id"`
	State          AttemptState `json:"state"`
	Snapshot       Snapshot     `json:"snapshot"`
	RemoteJobID    string       `json:"remote_job_id,omitempty"`
	RemoteStatus   string       `json:"remote_status,omitempty"`
	QueuePosition  int          `json:"queue_position,omitempty"`
	ResultAssetIDs []string     `json:"result_asset_ids"`
	Error          AttemptError `json:"error"`
	CreatedAt      time.Time    `json:"created_at"`
	StartedAt      time.Time    `json:"started_at,omitempty"`
	CompletedAt    time.Time    `json:"completed_at,omitempty"`
}

type AttemptError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type AttemptState string

const (
	AttemptQueued      AttemptState = "queued"
	AttemptSubmitting  AttemptState = "submitting"
	AttemptPolling     AttemptState = "polling"
	AttemptSucceeded   AttemptState = "succeeded"
	AttemptFailed      AttemptState = "failed"
	AttemptCancelled   AttemptState = "cancelled"
	AttemptInterrupted AttemptState = "interrupted"
)

type Filter struct {
	Query      string
	Folder     string
	ProviderID string
}

type CreateBatchInput struct {
	Title       string          `json:"title"`
	Folder      string          `json:"folder"`
	ProviderID  string          `json:"provider_id"`
	Concurrency int             `json:"concurrency"`
	BaseParams  json.RawMessage `json:"base_params"`
}

type UpdateBatchInput = CreateBatchInput

type CreateItemInput struct {
	Prompt         string          `json:"prompt"`
	NegativePrompt string          `json:"negative_prompt"`
	ParamsOverride json.RawMessage `json:"params_override"`
	InputAssets    InputAssets     `json:"input_assets"`
}

type UpdateItemInput = CreateItemInput

type CreateAttemptInput struct {
	State    AttemptState `json:"state"`
	Snapshot Snapshot     `json:"snapshot"`
}

type UpdateAttemptInput struct {
	State          AttemptState `json:"state"`
	RemoteJobID    string       `json:"remote_job_id"`
	RemoteStatus   string       `json:"remote_status"`
	QueuePosition  int          `json:"queue_position"`
	ResultAssetIDs []string     `json:"result_asset_ids"`
	Error          AttemptError `json:"error"`
}

type batchDocument struct {
	SchemaVersion int   `json:"schema_version"`
	Batch         Batch `json:"batch"`
}
