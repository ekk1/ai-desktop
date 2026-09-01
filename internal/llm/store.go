package llm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/store"
)

const runSchemaVersion = 1

type runDocument struct {
	SchemaVersion int `json:"schema_version"`
	Run           Run `json:"run"`
}

type RunStore struct {
	mu   sync.RWMutex
	root string
	runs map[string]Run
}

func OpenRunStore(sessionsRoot string) (*RunStore, error) {
	if err := os.MkdirAll(sessionsRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create run store root: %w", err)
	}
	runStore := &RunStore{root: sessionsRoot, runs: make(map[string]Run)}
	sessions, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return nil, fmt.Errorf("scan run store root: %w", err)
	}
	for _, sessionEntry := range sessions {
		if !sessionEntry.IsDir() || !safePathID(sessionEntry.Name()) {
			continue
		}
		runsDirectory := filepath.Join(sessionsRoot, sessionEntry.Name(), "runs")
		entries, err := os.ReadDir(runsDirectory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("scan runs for session %q: %w", sessionEntry.Name(), err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			runID := strings.TrimSuffix(entry.Name(), ".json")
			if !safePathID(runID) {
				continue
			}
			var document runDocument
			path := filepath.Join(runsDirectory, entry.Name())
			if err := store.ReadJSON(path, &document); err != nil {
				return nil, fmt.Errorf("load run %q: %w", runID, err)
			}
			if document.SchemaVersion != runSchemaVersion || document.Run.ID != runID || document.Run.SessionID != sessionEntry.Name() {
				return nil, fmt.Errorf("run %q has invalid schema or identity", runID)
			}
			if err := validateRun(document.Run); err != nil {
				return nil, fmt.Errorf("validate run %q: %w", runID, err)
			}
			if _, duplicate := runStore.runs[runID]; duplicate {
				return nil, fmt.Errorf("duplicate run ID %q", runID)
			}
			if document.Run.State == RunQueued || document.Run.State == RunRunning {
				document.Run.State = RunInterrupted
				document.Run.Error = RunError{Code: "interrupted", Message: "application restarted while the run was active"}
				document.Run.CompletedAt = time.Now().UTC()
				if err := store.WriteJSON(path, runDocument{SchemaVersion: runSchemaVersion, Run: document.Run}, 0o600); err != nil {
					return nil, fmt.Errorf("mark run %q interrupted: %w", runID, err)
				}
			}
			runStore.runs[runID] = cloneRun(document.Run)
		}
	}
	return runStore, nil
}

func (runStore *RunStore) Save(run Run) error {
	if err := validateRun(run); err != nil {
		return err
	}
	cloned := cloneRun(run)
	runStore.mu.Lock()
	defer runStore.mu.Unlock()
	path := runStore.runPath(run.SessionID, run.ID)
	if err := store.WriteJSON(path, runDocument{SchemaVersion: runSchemaVersion, Run: cloned}, 0o600); err != nil {
		return fmt.Errorf("save run: %w", err)
	}
	runStore.runs[run.ID] = cloned
	return nil
}

func (runStore *RunStore) Get(runID string) (Run, bool) {
	runStore.mu.RLock()
	defer runStore.mu.RUnlock()
	run, exists := runStore.runs[runID]
	if !exists {
		return Run{}, false
	}
	return cloneRun(run), true
}

func (runStore *RunStore) List(sessionID string) []Run {
	runStore.mu.RLock()
	defer runStore.mu.RUnlock()
	result := make([]Run, 0)
	for _, run := range runStore.runs {
		if sessionID == "" || run.SessionID == sessionID {
			result = append(result, cloneRun(run))
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.After(result[right].CreatedAt)
	})
	return result
}

func (runStore *RunStore) ForgetSession(sessionID string) {
	runStore.mu.Lock()
	defer runStore.mu.Unlock()
	for runID, run := range runStore.runs {
		if run.SessionID == sessionID {
			delete(runStore.runs, runID)
		}
	}
}

func (runStore *RunStore) runPath(sessionID, runID string) string {
	return filepath.Join(runStore.root, sessionID, "runs", runID+".json")
}

func validateRun(run Run) error {
	if !safePathID(run.ID) || !safePathID(run.SessionID) {
		return fmt.Errorf("run and session IDs must be safe non-empty path components")
	}
	switch run.State {
	case RunQueued, RunRunning, RunSucceeded, RunFailed, RunCancelled, RunInterrupted:
	default:
		return fmt.Errorf("unsupported run state %q", run.State)
	}
	return nil
}

func safePathID(value string) bool {
	if value == "" || value == "." || value == ".." || len(value) > 120 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._-", character) {
			continue
		}
		return false
	}
	return true
}

func cloneRun(run Run) Run {
	clone := run
	clone.Snapshot = cloneSnapshot(run.Snapshot)
	return clone
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	clone := snapshot
	clone.Panels = make([]PanelSnapshot, len(snapshot.Panels))
	for index, panel := range snapshot.Panels {
		clone.Panels[index] = panel
		clone.Panels[index].KnowledgeIDs = append([]string{}, panel.KnowledgeIDs...)
		clone.Panels[index].AssetIDs = append([]string{}, panel.AssetIDs...)
	}
	clone.Knowledge = make([]KnowledgeSnapshot, len(snapshot.Knowledge))
	for index, item := range snapshot.Knowledge {
		clone.Knowledge[index] = item
		clone.Knowledge[index].Tags = append([]string{}, item.Tags...)
		clone.Knowledge[index].AssetIDs = append([]string{}, item.AssetIDs...)
	}
	clone.Assets = append([]AssetSnapshot{}, snapshot.Assets...)
	clone.AssetDataURLs = append([]string{}, snapshot.AssetDataURLs...)
	clone.QuickPath.Params = cloneRawMessage(snapshot.QuickPath.Params)
	clone.Provider.Headers = cloneStringMap(snapshot.Provider.Headers)
	clone.Headers = cloneStringMap(snapshot.Headers)
	clone.Body = cloneRawMessage(snapshot.Body)
	return clone
}

func cloneRawMessage(source []byte) []byte {
	if source == nil {
		return nil
	}
	return append([]byte{}, source...)
}
