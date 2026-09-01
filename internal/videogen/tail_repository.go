package videogen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/store"
)

const tailExtractionSchemaVersion = 1

var ErrTailExtractionNotFound = errors.New("tail extraction not found")

type tailExtractionDocument struct {
	SchemaVersion int              `json:"schema_version"`
	Extractions   []TailExtraction `json:"extractions"`
}

// TailRepository keeps tail extraction records in one atomically replaced
// document because they are independent of video batches.
type TailRepository struct {
	mu          sync.RWMutex
	path        string
	extractions map[string]TailExtraction
}

func OpenTailRepository(path string) (*TailRepository, error) {
	if path == "" {
		return nil, fmt.Errorf("tail extraction repository path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create tail extraction directory: %w", err)
	}
	document := tailExtractionDocument{}
	err := store.ReadJSON(path, &document)
	if errors.Is(err, os.ErrNotExist) {
		document = tailExtractionDocument{SchemaVersion: tailExtractionSchemaVersion, Extractions: []TailExtraction{}}
		if err := store.WriteJSON(path, document, 0o600); err != nil {
			return nil, fmt.Errorf("create tail extraction repository: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read tail extraction repository: %w", err)
	}
	if document.SchemaVersion != tailExtractionSchemaVersion {
		return nil, fmt.Errorf("tail extraction schema version %d is unsupported", document.SchemaVersion)
	}
	repository := &TailRepository{path: path, extractions: make(map[string]TailExtraction, len(document.Extractions))}
	changed := false
	for _, extraction := range document.Extractions {
		if err := validateTailExtraction(extraction); err != nil {
			return nil, err
		}
		if _, duplicate := repository.extractions[extraction.ID]; duplicate {
			return nil, fmt.Errorf("duplicate tail extraction %q", extraction.ID)
		}
		if extraction.State == AttemptQueued || extraction.State == AttemptRunning {
			now := time.Now().UTC()
			extraction.State = AttemptInterrupted
			extraction.CompletedAt = &now
			extraction.Error = AttemptError{Code: "workbench_restarted", Message: "workbench restarted before the tail extraction completed"}
			changed = true
		}
		repository.extractions[extraction.ID] = cloneTailExtraction(extraction)
	}
	if changed {
		if err := repository.saveLocked(); err != nil {
			return nil, fmt.Errorf("recover active tail extractions: %w", err)
		}
	}
	return repository, nil
}

func (repository *TailRepository) Create(extraction TailExtraction) error {
	if err := validateTailExtraction(extraction); err != nil {
		return err
	}
	if extraction.State != AttemptQueued {
		return fmt.Errorf("new tail extraction must be queued")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.extractions[extraction.ID]; exists {
		return fmt.Errorf("tail extraction %q already exists", extraction.ID)
	}
	repository.extractions[extraction.ID] = cloneTailExtraction(extraction)
	if err := repository.saveLocked(); err != nil {
		delete(repository.extractions, extraction.ID)
		return err
	}
	return nil
}

func (repository *TailRepository) Get(id string) (TailExtraction, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	extraction, ok := repository.extractions[id]
	return cloneTailExtraction(extraction), ok
}

func (repository *TailRepository) List() []TailExtraction {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	items := make([]TailExtraction, 0, len(repository.extractions))
	for _, extraction := range repository.extractions {
		items = append(items, cloneTailExtraction(extraction))
	}
	sort.Slice(items, func(left, right int) bool { return items[left].CreatedAt.After(items[right].CreatedAt) })
	return items
}

func (repository *TailRepository) Update(extraction TailExtraction) error {
	if err := validateTailExtraction(extraction); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	previous, exists := repository.extractions[extraction.ID]
	if !exists {
		return ErrTailExtractionNotFound
	}
	if extraction.SourceAssetID != previous.SourceAssetID || extraction.PresetID != previous.PresetID || !extraction.CreatedAt.Equal(previous.CreatedAt) {
		return fmt.Errorf("tail extraction identity is immutable")
	}
	if terminalAttemptState(previous.State) {
		return fmt.Errorf("terminal tail extraction is immutable")
	}
	if !allowedTailTransition(previous.State, extraction.State) {
		return fmt.Errorf("invalid tail extraction state transition from %q to %q", previous.State, extraction.State)
	}
	repository.extractions[extraction.ID] = cloneTailExtraction(extraction)
	if err := repository.saveLocked(); err != nil {
		repository.extractions[extraction.ID] = previous
		return err
	}
	return nil
}

func (repository *TailRepository) saveLocked() error {
	items := make([]TailExtraction, 0, len(repository.extractions))
	for _, extraction := range repository.extractions {
		items = append(items, cloneTailExtraction(extraction))
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	return store.WriteJSON(repository.path, tailExtractionDocument{SchemaVersion: tailExtractionSchemaVersion, Extractions: items}, 0o600)
}

func validateTailExtraction(extraction TailExtraction) error {
	if !validGeneratedID(extraction.ID) || !validGeneratedID(extraction.SourceAssetID) || !validPresetID(extraction.PresetID) || !knownTailExtractionState(extraction.State) || extraction.CreatedAt.IsZero() {
		return fmt.Errorf("tail extraction is invalid")
	}
	if extraction.State == AttemptSucceeded && !validGeneratedID(extraction.OutputAssetID) {
		return fmt.Errorf("successful tail extraction result is invalid")
	}
	return nil
}

func knownTailExtractionState(state AttemptState) bool {
	return state == AttemptQueued || state == AttemptRunning || state == AttemptSucceeded || state == AttemptFailed || state == AttemptCancelled || state == AttemptInterrupted
}

func allowedTailTransition(from, to AttemptState) bool {
	if from == to {
		return true
	}
	switch from {
	case AttemptQueued:
		return to == AttemptRunning || to == AttemptFailed || to == AttemptCancelled || to == AttemptInterrupted
	case AttemptRunning:
		return to == AttemptSucceeded || to == AttemptFailed || to == AttemptCancelled || to == AttemptInterrupted
	default:
		return false
	}
}

func cloneTailExtraction(extraction TailExtraction) TailExtraction {
	extraction.StartedAt = cloneTime(extraction.StartedAt)
	extraction.CompletedAt = cloneTime(extraction.CompletedAt)
	return extraction
}
