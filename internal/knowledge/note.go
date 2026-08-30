package knowledge

import (
	"fmt"
	"strings"
	"time"
)

type Note struct {
	ID        string    `json:"id"`
	Folder    string    `json:"folder,omitempty"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags"`
	AssetIDs  []string  `json:"asset_ids"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Input struct {
	Folder   string   `json:"folder"`
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	Tags     []string `json:"tags"`
	AssetIDs []string `json:"asset_ids"`
}

type Filter struct {
	Folder string
	Query  string
}

func normalizeInput(input Input) (Input, error) {
	input.Folder = strings.TrimSpace(input.Folder)
	input.Title = strings.TrimSpace(input.Title)
	if input.Title == "" {
		return Input{}, fmt.Errorf("title is required")
	}
	input.Tags = uniqueStrings(input.Tags)
	input.AssetIDs = uniqueStrings(input.AssetIDs)
	return input, nil
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneNote(source Note) Note {
	clone := source
	clone.Tags = append([]string(nil), source.Tags...)
	clone.AssetIDs = append([]string(nil), source.AssetIDs...)
	return clone
}

func cloneNotes(source []Note) []Note {
	result := make([]Note, len(source))
	for index, note := range source {
		result[index] = cloneNote(note)
	}
	return result
}
