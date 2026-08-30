package llm

import "time"

type RunState string

const (
	RunQueued      RunState = "queued"
	RunRunning     RunState = "running"
	RunSucceeded   RunState = "succeeded"
	RunFailed      RunState = "failed"
	RunCancelled   RunState = "cancelled"
	RunInterrupted RunState = "interrupted"
)

type RunError struct {
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Run struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	ParentPanelID string    `json:"parent_panel_id"`
	QuickPathID   string    `json:"quick_path_id"`
	State         RunState  `json:"state"`
	Snapshot      Snapshot  `json:"snapshot"`
	Output        string    `json:"output"`
	StatusCode    int       `json:"status_code,omitempty"`
	ResultPanelID string    `json:"result_panel_id,omitempty"`
	Error         RunError  `json:"error"`
	CreatedAt     time.Time `json:"created_at"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
}

func (state RunState) terminal() bool {
	switch state {
	case RunSucceeded, RunFailed, RunCancelled, RunInterrupted:
		return true
	default:
		return false
	}
}
