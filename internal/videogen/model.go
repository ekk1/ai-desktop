package videogen

import (
	"encoding/json"
	"time"

	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

const batchSchemaVersion = 1

type TimingInput struct {
	Mode            string  `json:"mode"`
	VideoFrames     int     `json:"video_frames,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	FPS             int     `json:"fps"`
}

type ResolvedTiming struct {
	InputMode        string  `json:"input_mode"`
	DurationSeconds  float64 `json:"duration_seconds,omitempty"`
	FPS              int     `json:"fps"`
	RequestedFrames  int     `json:"requested_frames"`
	AlgorithmVersion string  `json:"algorithm_version"`
}

type SelectedAsset struct {
	AssetID string `json:"asset_id"`
	Role    string `json:"role"`
	Order   int    `json:"order"`
}

type Item struct {
	ID              string          `json:"id"`
	Order           int             `json:"order"`
	Prompt          string          `json:"prompt"`
	NegativePrompt  string          `json:"negative_prompt"`
	Enabled         bool            `json:"enabled"`
	ParamsOverride  json.RawMessage `json:"params_override"`
	TimingOverride  *TimingInput    `json:"timing_override,omitempty"`
	InitImageID     string          `json:"init_image_id,omitempty"`
	EndImageID      string          `json:"end_image_id,omitempty"`
	ControlFrameIDs []string        `json:"control_frame_ids"`
	SelectedAssets  []SelectedAsset `json:"selected_assets"`
	Attempts        []Attempt       `json:"attempts"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

type Batch struct {
	ID            string                    `json:"id"`
	Folder        string                    `json:"folder"`
	Title         string                    `json:"title"`
	PresetID      string                    `json:"preset_id"`
	ExecutionKind videoconfig.ExecutionKind `json:"execution_kind"`
	CommonParams  json.RawMessage           `json:"common_params"`
	Timing        TimingInput               `json:"timing"`
	Concurrency   int                       `json:"concurrency"`
	Items         []Item                    `json:"items"`
	CreatedAt     time.Time                 `json:"created_at"`
	UpdatedAt     time.Time                 `json:"updated_at"`
}

type AssetSnapshot struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	MediaType   string `json:"media_type"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Size        int64  `json:"size"`
	Order       int    `json:"order"`
}

type Snapshot struct {
	ExecutionKind  videoconfig.ExecutionKind `json:"execution_kind"`
	HTTPProvider   *videoconfig.HTTPProvider `json:"http_provider,omitempty"`
	CLIPreset      *videoconfig.CLIPreset    `json:"cli_preset,omitempty"`
	Params         json.RawMessage           `json:"params"`
	Prompt         string                    `json:"prompt"`
	NegativePrompt string                    `json:"negative_prompt"`
	Timing         ResolvedTiming            `json:"timing"`
	InputAssets    []AssetSnapshot           `json:"input_assets"`
	CreatedAt      time.Time                 `json:"created_at"`
}

type AttemptError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Attempt struct {
	ID                    string                    `json:"id"`
	BatchID               string                    `json:"batch_id"`
	ItemID                string                    `json:"item_id"`
	ExecutionKind         videoconfig.ExecutionKind `json:"execution_kind"`
	State                 AttemptState              `json:"state"`
	Snapshot              Snapshot                  `json:"snapshot"`
	RemoteJobID           string                    `json:"remote_job_id,omitempty"`
	RemoteStatus          string                    `json:"remote_status,omitempty"`
	QueuePosition         int                       `json:"queue_position,omitempty"`
	PID                   int                       `json:"pid,omitempty"`
	ActualFrameCount      int                       `json:"actual_frame_count,omitempty"`
	WorkspaceRelativePath string                    `json:"workspace_relative_path,omitempty"`
	OutputAssetID         string                    `json:"output_asset_id,omitempty"`
	Error                 AttemptError              `json:"error"`
	CreatedAt             time.Time                 `json:"created_at"`
	StartedAt             *time.Time                `json:"started_at,omitempty"`
	CompletedAt           *time.Time                `json:"completed_at,omitempty"`
}

type AttemptState string

const (
	AttemptQueued      AttemptState = "queued"
	AttemptSubmitting  AttemptState = "submitting"
	AttemptPolling     AttemptState = "polling"
	AttemptRunning     AttemptState = "running"
	AttemptSucceeded   AttemptState = "succeeded"
	AttemptFailed      AttemptState = "failed"
	AttemptCancelled   AttemptState = "cancelled"
	AttemptInterrupted AttemptState = "interrupted"
)

type Filter struct {
	Query         string
	Folder        string
	PresetID      string
	ExecutionKind videoconfig.ExecutionKind
}
type CreateBatchInput struct {
	Title         string                    `json:"title"`
	Folder        string                    `json:"folder"`
	ExecutionKind videoconfig.ExecutionKind `json:"execution_kind"`
	PresetID      string                    `json:"preset_id"`
	Concurrency   int                       `json:"concurrency"`
	CommonParams  json.RawMessage           `json:"common_params"`
	Timing        TimingInput               `json:"timing"`
}
type UpdateBatchInput = CreateBatchInput
type CreateItemInput struct {
	Prompt          string          `json:"prompt"`
	NegativePrompt  string          `json:"negative_prompt"`
	Enabled         bool            `json:"enabled"`
	ParamsOverride  json.RawMessage `json:"params_override"`
	TimingOverride  *TimingInput    `json:"timing_override,omitempty"`
	InitImageID     string          `json:"init_image_id,omitempty"`
	EndImageID      string          `json:"end_image_id,omitempty"`
	ControlFrameIDs []string        `json:"control_frame_ids"`
	SelectedAssets  []SelectedAsset `json:"selected_assets"`
}
type UpdateItemInput = CreateItemInput
type CreateAttemptInput struct {
	State    AttemptState `json:"state"`
	Snapshot Snapshot     `json:"snapshot"`
}
type UpdateAttemptInput struct {
	State                 AttemptState `json:"state"`
	RemoteJobID           string       `json:"remote_job_id"`
	RemoteStatus          string       `json:"remote_status"`
	QueuePosition         int          `json:"queue_position"`
	PID                   int          `json:"pid"`
	ActualFrameCount      int          `json:"actual_frame_count"`
	WorkspaceRelativePath string       `json:"workspace_relative_path"`
	OutputAssetID         string       `json:"output_asset_id"`
	Error                 AttemptError `json:"error"`
}
type batchDocument struct {
	SchemaVersion int   `json:"schema_version"`
	Batch         Batch `json:"batch"`
}
