package asset

import (
	"fmt"
	"io"
	"strings"
	"time"
)

type State string

const (
	StateActive  State = "active"
	StateArchive State = "archive"
)

type Reference struct {
	Module   string `json:"module"`
	RecordID string `json:"record_id"`
}

type Asset struct {
	ID          string      `json:"id"`
	SHA256      string      `json:"sha256"`
	MediaType   string      `json:"media_type"`
	DisplayName string      `json:"display_name"`
	StoredName  string      `json:"stored_name"`
	Size        int64       `json:"size"`
	Width       int         `json:"width,omitempty"`
	Height      int         `json:"height,omitempty"`
	State       State       `json:"state"`
	Source      string      `json:"source,omitempty"`
	Notes       string      `json:"notes,omitempty"`
	References  []Reference `json:"references,omitempty"`
	CreatedAt   time.Time   `json:"created_at"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

type ImportInput struct {
	Reader      io.Reader
	DisplayName string
	MediaType   string
	Source      string
}

type Filter struct {
	State     State
	MediaType string
	Query     string
}

func validateState(state State) error {
	if state != StateActive && state != StateArchive {
		return fmt.Errorf("unsupported asset state %q", state)
	}
	return nil
}

func cloneAsset(source Asset) Asset {
	clone := source
	clone.References = append([]Reference(nil), source.References...)
	return clone
}

func matchesFilter(asset Asset, filter Filter) bool {
	if filter.State != "" && asset.State != filter.State {
		return false
	}
	if filter.MediaType != "" && !strings.HasPrefix(asset.MediaType, filter.MediaType) {
		return false
	}
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	if query == "" {
		return true
	}
	searchable := strings.ToLower(asset.DisplayName + "\n" + asset.Notes + "\n" + asset.Source)
	return strings.Contains(searchable, query)
}
