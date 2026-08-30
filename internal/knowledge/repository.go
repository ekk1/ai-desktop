package knowledge

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/store"
)

const schemaVersion = 1

var ErrNotFound = errors.New("knowledge note not found")

type document struct {
	SchemaVersion int    `json:"schema_version"`
	Notes         []Note `json:"notes"`
}

type Repository struct {
	mu    sync.RWMutex
	path  string
	notes []Note
}

func OpenRepository(path string) (*Repository, error) {
	stored := document{}
	err := store.ReadJSON(path, &stored)
	if errors.Is(err, os.ErrNotExist) {
		stored = document{SchemaVersion: schemaVersion, Notes: []Note{}}
		if err := store.WriteJSON(path, stored, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if stored.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("knowledge schema version %d is unsupported", stored.SchemaVersion)
	}
	return &Repository{path: path, notes: cloneNotes(stored.Notes)}, nil
}

func (repository *Repository) Create(input Input) (Note, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return Note{}, err
	}
	id, err := randomNoteID()
	if err != nil {
		return Note{}, err
	}
	now := time.Now().UTC()
	created := Note{
		ID: id, Folder: normalized.Folder, Title: normalized.Title, Content: normalized.Content,
		Tags: normalized.Tags, AssetIDs: normalized.AssetIDs, CreatedAt: now, UpdatedAt: now,
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := append(cloneNotes(repository.notes), created)
	if err := repository.save(updated); err != nil {
		return Note{}, err
	}
	repository.notes = updated
	return cloneNote(created), nil
}

func (repository *Repository) List(filter Filter) []Note {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	result := make([]Note, 0, len(repository.notes))
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	for _, note := range repository.notes {
		if filter.Folder != "" && note.Folder != filter.Folder {
			continue
		}
		if query != "" {
			searchable := strings.ToLower(note.Folder + "\n" + note.Title + "\n" + note.Content + "\n" + strings.Join(note.Tags, "\n"))
			if !strings.Contains(searchable, query) {
				continue
			}
		}
		result = append(result, cloneNote(note))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result
}

func (repository *Repository) Get(id string) (Note, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	for _, note := range repository.notes {
		if note.ID == id {
			return cloneNote(note), true
		}
	}
	return Note{}, false
}

func (repository *Repository) Update(id string, input Input) (Note, error) {
	normalized, err := normalizeInput(input)
	if err != nil {
		return Note{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := cloneNotes(repository.notes)
	for index := range updated {
		if updated[index].ID != id {
			continue
		}
		now := time.Now().UTC()
		if !now.After(updated[index].UpdatedAt) {
			now = updated[index].UpdatedAt.Add(time.Nanosecond)
		}
		updated[index].Folder = normalized.Folder
		updated[index].Title = normalized.Title
		updated[index].Content = normalized.Content
		updated[index].Tags = append([]string(nil), normalized.Tags...)
		updated[index].AssetIDs = append([]string(nil), normalized.AssetIDs...)
		updated[index].UpdatedAt = now
		if err := repository.save(updated); err != nil {
			return Note{}, err
		}
		repository.notes = updated
		return cloneNote(updated[index]), nil
	}
	return Note{}, ErrNotFound
}

func (repository *Repository) Delete(id string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	for index := range repository.notes {
		if repository.notes[index].ID != id {
			continue
		}
		updated := append(cloneNotes(repository.notes[:index]), cloneNotes(repository.notes[index+1:])...)
		if err := repository.save(updated); err != nil {
			return err
		}
		repository.notes = updated
		return nil
	}
	return ErrNotFound
}

func (repository *Repository) restore(note Note) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	updated := cloneNotes(repository.notes)
	for index := range updated {
		if updated[index].ID == note.ID {
			updated[index] = cloneNote(note)
			if err := repository.save(updated); err != nil {
				return err
			}
			repository.notes = updated
			return nil
		}
	}
	return ErrNotFound
}

func (repository *Repository) save(notes []Note) error {
	return store.WriteJSON(repository.path, document{SchemaVersion: schemaVersion, Notes: notes}, 0o600)
}

func randomNoteID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate knowledge note ID: %w", err)
	}
	return hex.EncodeToString(value), nil
}
