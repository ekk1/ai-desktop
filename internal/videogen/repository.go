package videogen

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ekk1/ai-desktop/internal/store"
	"github.com/ekk1/ai-desktop/internal/videoconfig"
)

var (
	ErrBatchNotFound            = errors.New("video batch not found")
	ErrItemNotFound             = errors.New("video item not found")
	ErrAttemptNotFound          = errors.New("video attempt not found")
	ErrMoveBoundary             = errors.New("video item move exceeds batch boundary")
	ErrActiveAttempt            = errors.New("video item already has an active attempt")
	ErrInvalidAttemptTransition = errors.New("invalid video attempt state transition")
)

type Repository struct {
	mu      sync.RWMutex
	root    string
	batches map[string]Batch
}

func OpenRepository(root string) (*Repository, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create video batch directory: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read video batch directory: %w", err)
	}
	r := &Repository{root: root, batches: make(map[string]Batch)}
	for _, entry := range entries {
		if !entry.IsDir() || !validGeneratedID(entry.Name()) {
			continue
		}
		var document batchDocument
		if err := store.ReadJSON(filepath.Join(root, entry.Name(), "batch.json"), &document); err != nil {
			return nil, fmt.Errorf("load video batch %q: %w", entry.Name(), err)
		}
		if err := validateDocument(document, entry.Name()); err != nil {
			return nil, fmt.Errorf("validate video batch %q: %w", entry.Name(), err)
		}
		document.Batch = canonicalBatch(document.Batch)
		if interruptActiveAttempts(&document.Batch) {
			if err := r.save(document.Batch); err != nil {
				return nil, fmt.Errorf("recover video batch %q: %w", entry.Name(), err)
			}
		}
		r.batches[document.Batch.ID] = cloneBatch(document.Batch)
	}
	return r, nil
}

func (r *Repository) CreateBatch(input CreateBatchInput) (Batch, error) {
	input, err := normalizeBatchInput(input)
	if err != nil {
		return Batch{}, err
	}
	id, err := randomID()
	if err != nil {
		return Batch{}, err
	}
	now := time.Now().UTC()
	batch := Batch{ID: id, Title: input.Title, Folder: input.Folder, ExecutionKind: input.ExecutionKind, PresetID: input.PresetID, Concurrency: input.Concurrency, CommonParams: input.CommonParams, Timing: input.Timing, Items: []Item{}, CreatedAt: now, UpdatedAt: now}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.save(batch); err != nil {
		return Batch{}, err
	}
	r.batches[id] = cloneBatch(batch)
	return cloneBatch(batch), nil
}

func (r *Repository) List(filter Filter) []Batch {
	r.mu.RLock()
	defer r.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	out := make([]Batch, 0, len(r.batches))
	for _, batch := range r.batches {
		if filter.Folder != "" && batch.Folder != filter.Folder || filter.PresetID != "" && batch.PresetID != filter.PresetID || filter.ExecutionKind != "" && batch.ExecutionKind != filter.ExecutionKind || query != "" && !strings.Contains(strings.ToLower(batch.Title+"\n"+batch.Folder), query) {
			continue
		}
		out = append(out, cloneBatch(batch))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out
}

func (r *Repository) Get(batchID string) (Batch, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	batch, ok := r.batches[batchID]
	return cloneBatch(batch), ok
}

func (r *Repository) UpdateBatch(batchID string, input UpdateBatchInput) (Batch, error) {
	input, err := normalizeBatchInput(input)
	if err != nil {
		return Batch{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return Batch{}, ErrBatchNotFound
	}
	updated := cloneBatch(batch)
	updated.Title, updated.Folder, updated.ExecutionKind, updated.PresetID = input.Title, input.Folder, input.ExecutionKind, input.PresetID
	updated.Concurrency, updated.CommonParams, updated.Timing = input.Concurrency, input.CommonParams, input.Timing
	updated.UpdatedAt = nextTime(batch.UpdatedAt)
	if err := r.save(updated); err != nil {
		return Batch{}, err
	}
	r.batches[batchID] = updated
	return cloneBatch(updated), nil
}

func (r *Repository) DeleteBatch(batchID string) error {
	if !validGeneratedID(batchID) {
		return ErrBatchNotFound
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.batches[batchID]; !ok {
		return ErrBatchNotFound
	}
	if err := os.RemoveAll(filepath.Join(r.root, batchID)); err != nil {
		return fmt.Errorf("delete video batch: %w", err)
	}
	delete(r.batches, batchID)
	return nil
}

func (r *Repository) CreateItems(batchID string, inputs []CreateItemInput) ([]Item, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("at least one video item is required")
	}
	normalized := make([]CreateItemInput, len(inputs))
	for i := range inputs {
		var err error
		normalized[i], err = normalizeItemInput(inputs[i])
		if err != nil {
			return nil, fmt.Errorf("video item %d: %w", i, err)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return nil, ErrBatchNotFound
	}
	updated := cloneBatch(batch)
	created := make([]Item, 0, len(normalized))
	for _, input := range normalized {
		id, err := randomID()
		if err != nil {
			return nil, err
		}
		now := nextTime(updated.UpdatedAt)
		item := itemFromInput(id, len(updated.Items), input, now)
		updated.Items = append(updated.Items, item)
		created = append(created, cloneItem(item))
		updated.UpdatedAt = now
	}
	if err := r.save(updated); err != nil {
		return nil, err
	}
	r.batches[batchID] = updated
	return created, nil
}

func (r *Repository) UpdateItem(batchID, itemID string, input UpdateItemInput) (Item, error) {
	input, err := normalizeItemInput(input)
	if err != nil {
		return Item{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return Item{}, ErrBatchNotFound
	}
	pos := itemIndex(batch.Items, itemID)
	if pos < 0 {
		return Item{}, ErrItemNotFound
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	item := &updated.Items[pos]
	item.Prompt, item.NegativePrompt, item.Enabled, item.ParamsOverride = input.Prompt, input.NegativePrompt, input.Enabled, input.ParamsOverride
	item.TimingOverride, item.InitImageID, item.EndImageID = cloneTiming(input.TimingOverride), input.InitImageID, input.EndImageID
	item.ControlFrameIDs, item.SelectedAssets = append([]string(nil), input.ControlFrameIDs...), append([]SelectedAsset(nil), input.SelectedAssets...)
	item.UpdatedAt, updated.UpdatedAt = now, now
	if err := r.save(updated); err != nil {
		return Item{}, err
	}
	r.batches[batchID] = updated
	return cloneItem(*item), nil
}

func (r *Repository) DeleteItem(batchID, itemID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return ErrBatchNotFound
	}
	pos := itemIndex(batch.Items, itemID)
	if pos < 0 {
		return ErrItemNotFound
	}
	updated := cloneBatch(batch)
	updated.Items = append(updated.Items[:pos], updated.Items[pos+1:]...)
	for i := range updated.Items {
		updated.Items[i].Order = i
	}
	updated.UpdatedAt = nextTime(batch.UpdatedAt)
	if err := r.save(updated); err != nil {
		return err
	}
	r.batches[batchID] = updated
	return nil
}

func (r *Repository) MoveItem(batchID, itemID string, offset int) (Batch, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return Batch{}, ErrBatchNotFound
	}
	pos := itemIndex(batch.Items, itemID)
	if pos < 0 {
		return Batch{}, ErrItemNotFound
	}
	target := pos + offset
	if offset == 0 || target < 0 || target >= len(batch.Items) {
		return Batch{}, ErrMoveBoundary
	}
	updated := cloneBatch(batch)
	moving := updated.Items[pos]
	if target < pos {
		copy(updated.Items[target+1:pos+1], updated.Items[target:pos])
	} else {
		copy(updated.Items[pos:target], updated.Items[pos+1:target+1])
	}
	updated.Items[target] = moving
	for i := range updated.Items {
		updated.Items[i].Order = i
	}
	updated.UpdatedAt = nextTime(batch.UpdatedAt)
	if err := r.save(updated); err != nil {
		return Batch{}, err
	}
	r.batches[batchID] = updated
	return cloneBatch(updated), nil
}

func (r *Repository) CreateAttempt(batchID, itemID string, input CreateAttemptInput) (Attempt, error) {
	if input.State != AttemptQueued {
		return Attempt{}, fmt.Errorf("new video attempt state must be %q", AttemptQueued)
	}
	snapshot, err := normalizeSnapshot(input.Snapshot)
	if err != nil {
		return Attempt{}, err
	}
	id, err := randomID()
	if err != nil {
		return Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	if snapshot.ExecutionKind != batch.ExecutionKind {
		return Attempt{}, fmt.Errorf("attempt snapshot execution kind does not match batch")
	}
	itemPos := itemIndex(batch.Items, itemID)
	if itemPos < 0 {
		return Attempt{}, ErrItemNotFound
	}
	for _, attempt := range batch.Items[itemPos].Attempts {
		if activeAttemptState(attempt.State) {
			return Attempt{}, ErrActiveAttempt
		}
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	snapshot.CreatedAt = now
	created := Attempt{ID: id, BatchID: batchID, ItemID: itemID, ExecutionKind: batch.ExecutionKind, State: AttemptQueued, Snapshot: snapshot, CreatedAt: now}
	updated.Items[itemPos].Attempts = append(updated.Items[itemPos].Attempts, created)
	updated.Items[itemPos].UpdatedAt, updated.UpdatedAt = now, now
	if err := r.save(updated); err != nil {
		return Attempt{}, err
	}
	r.batches[batchID] = updated
	return cloneAttempt(created), nil
}

func (r *Repository) UpdateAttempt(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
	input, err := normalizeAttemptUpdate(input)
	if err != nil {
		return Attempt{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	itemPos := itemIndex(batch.Items, itemID)
	if itemPos < 0 {
		return Attempt{}, ErrItemNotFound
	}
	attemptPos := attemptIndex(batch.Items[itemPos].Attempts, attemptID)
	if attemptPos < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	current := batch.Items[itemPos].Attempts[attemptPos]
	if !allowedAttemptTransition(current.State, input.State) {
		return Attempt{}, ErrInvalidAttemptTransition
	}
	if input.OutputAssetID != "" && input.OutputAssetID != current.OutputAssetID && r.resultAssetAttached(input.OutputAssetID) {
		return Attempt{}, fmt.Errorf("video attempt result asset ID is already attached")
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	attempt := &updated.Items[itemPos].Attempts[attemptPos]
	attempt.State, attempt.RemoteJobID, attempt.RemoteStatus, attempt.QueuePosition = input.State, input.RemoteJobID, input.RemoteStatus, input.QueuePosition
	attempt.PID, attempt.ActualFrameCount, attempt.WorkspaceRelativePath, attempt.Error = input.PID, input.ActualFrameCount, input.WorkspaceRelativePath, input.Error
	if input.OutputAssetID != "" {
		attempt.OutputAssetID = input.OutputAssetID
	}
	if (input.State == AttemptSubmitting || input.State == AttemptPolling || input.State == AttemptRunning) && attempt.StartedAt == nil {
		value := now
		attempt.StartedAt = &value
	}
	if terminalAttemptState(input.State) {
		value := now
		attempt.CompletedAt = &value
	}
	updated.Items[itemPos].UpdatedAt, updated.UpdatedAt = now, now
	if err := r.save(updated); err != nil {
		return Attempt{}, err
	}
	r.batches[batchID] = updated
	return cloneAttempt(*attempt), nil
}

func (r *Repository) AttachResult(batchID, itemID, attemptID, assetID string) (Attempt, error) {
	if !validGeneratedID(assetID) {
		return Attempt{}, fmt.Errorf("video attempt result asset ID is invalid")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	batch, ok := r.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	itemPos := itemIndex(batch.Items, itemID)
	if itemPos < 0 {
		return Attempt{}, ErrItemNotFound
	}
	attemptPos := attemptIndex(batch.Items[itemPos].Attempts, attemptID)
	if attemptPos < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	current := batch.Items[itemPos].Attempts[attemptPos]
	if terminalAttemptState(current.State) {
		return Attempt{}, ErrInvalidAttemptTransition
	}
	if current.OutputAssetID == assetID {
		return cloneAttempt(current), nil
	}
	if r.resultAssetAttached(assetID) {
		return Attempt{}, fmt.Errorf("video attempt result asset ID is already attached")
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	attempt := &updated.Items[itemPos].Attempts[attemptPos]
	if attempt.OutputAssetID != "" {
		return Attempt{}, fmt.Errorf("video attempt already has a result asset")
	}
	attempt.OutputAssetID = assetID
	updated.Items[itemPos].UpdatedAt, updated.UpdatedAt = now, now
	if err := r.save(updated); err != nil {
		return Attempt{}, err
	}
	r.batches[batchID] = updated
	return cloneAttempt(*attempt), nil
}

func (r *Repository) save(batch Batch) error {
	return store.WriteJSON(filepath.Join(r.root, batch.ID, "batch.json"), batchDocument{SchemaVersion: batchSchemaVersion, Batch: cloneBatch(batch)}, 0o600)
}

func normalizeBatchInput(input CreateBatchInput) (CreateBatchInput, error) {
	input.Title, input.Folder, input.PresetID = strings.TrimSpace(input.Title), strings.TrimSpace(input.Folder), strings.TrimSpace(input.PresetID)
	if input.Title == "" {
		return CreateBatchInput{}, fmt.Errorf("video batch title is required")
	}
	if !knownExecutionKind(input.ExecutionKind) {
		return CreateBatchInput{}, fmt.Errorf("video execution kind is invalid")
	}
	if !validPresetID(input.PresetID) {
		return CreateBatchInput{}, fmt.Errorf("video preset ID is invalid")
	}
	if input.Concurrency < 1 || input.Concurrency > 16 {
		return CreateBatchInput{}, fmt.Errorf("video batch concurrency must be between 1 and 16")
	}
	params, err := normalizeParams(input.CommonParams)
	if err != nil {
		return CreateBatchInput{}, fmt.Errorf("common params: %w", err)
	}
	timing, err := normalizeTiming(input.Timing)
	if err != nil {
		return CreateBatchInput{}, fmt.Errorf("timing: %w", err)
	}
	input.CommonParams, input.Timing = params, timing
	return input, nil
}

func normalizeItemInput(input CreateItemInput) (CreateItemInput, error) {
	params, err := normalizeParams(input.ParamsOverride)
	if err != nil {
		return CreateItemInput{}, fmt.Errorf("params override: %w", err)
	}
	if input.TimingOverride != nil {
		timing, err := normalizeTiming(*input.TimingOverride)
		if err != nil {
			return CreateItemInput{}, fmt.Errorf("timing override: %w", err)
		}
		input.TimingOverride = &timing
	}
	for name, id := range map[string]string{"init image": input.InitImageID, "end image": input.EndImageID} {
		if id != "" && !validGeneratedID(id) {
			return CreateItemInput{}, fmt.Errorf("%s ID is invalid", name)
		}
	}
	seen := map[string]struct{}{}
	frames := make([]string, 0, len(input.ControlFrameIDs))
	for _, id := range input.ControlFrameIDs {
		if !validGeneratedID(id) {
			return CreateItemInput{}, fmt.Errorf("control frame ID is invalid")
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			frames = append(frames, id)
		}
	}
	assets := append([]SelectedAsset(nil), input.SelectedAssets...)
	for i := range assets {
		if !validGeneratedID(assets[i].AssetID) || strings.TrimSpace(assets[i].Role) == "" || assets[i].Order != i {
			return CreateItemInput{}, fmt.Errorf("selected asset %d is invalid", i)
		}
	}
	input.ParamsOverride, input.ControlFrameIDs, input.SelectedAssets = params, frames, assets
	return input, nil
}

func normalizeParams(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, fmt.Errorf("must be one JSON Object")
	}
	for key := range object {
		if videoconfig.IsManagedVideoParam(key) {
			return nil, fmt.Errorf("%q is managed by the workbench", key)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("must be one JSON Object")
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

func normalizeTiming(input TimingInput) (TimingInput, error) {
	input.Mode = strings.TrimSpace(input.Mode)
	if _, err := ResolveTiming(input, nil); err != nil {
		return TimingInput{}, err
	}
	return input, nil
}

func normalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	if !knownExecutionKind(snapshot.ExecutionKind) {
		return Snapshot{}, fmt.Errorf("attempt snapshot execution kind is invalid")
	}
	if snapshot.ExecutionKind == videoconfig.ExecutionHTTP {
		if snapshot.HTTPProvider == nil || snapshot.CLIPreset != nil {
			return Snapshot{}, fmt.Errorf("HTTP snapshot requires only an HTTP provider")
		}
		if err := (videoconfig.Config{HTTPProviders: []videoconfig.HTTPProvider{*snapshot.HTTPProvider}}).Validate(); err != nil {
			return Snapshot{}, fmt.Errorf("attempt snapshot provider: %w", err)
		}
	} else {
		if snapshot.CLIPreset == nil || snapshot.HTTPProvider != nil {
			return Snapshot{}, fmt.Errorf("CLI snapshot requires only a CLI preset")
		}
		if err := (videoconfig.Config{CLIPresets: []videoconfig.CLIPreset{*snapshot.CLIPreset}}).Validate(); err != nil {
			return Snapshot{}, fmt.Errorf("attempt snapshot CLI preset: %w", err)
		}
	}
	params, err := normalizeParams(snapshot.Params)
	if err != nil {
		return Snapshot{}, fmt.Errorf("attempt snapshot params: %w", err)
	}
	if err := validateResolvedTiming(snapshot.Timing); err != nil {
		return Snapshot{}, err
	}
	for i := range snapshot.InputAssets {
		asset := snapshot.InputAssets[i]
		if !validGeneratedID(asset.ID) || len(asset.SHA256) != 64 {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d identity is invalid", i)
		}
		if _, err := hex.DecodeString(asset.SHA256); err != nil {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d SHA-256 is invalid", i)
		}
		if strings.TrimSpace(asset.MediaType) == "" || strings.TrimSpace(asset.DisplayName) == "" || strings.TrimSpace(asset.Role) == "" || asset.Size < 0 || asset.Order != i {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d metadata is invalid", i)
		}
	}
	snapshot.Params = params
	snapshot.InputAssets = append([]AssetSnapshot(nil), snapshot.InputAssets...)
	if snapshot.HTTPProvider != nil {
		provider := (videoconfig.Config{HTTPProviders: []videoconfig.HTTPProvider{*snapshot.HTTPProvider}}).Clone().HTTPProviders[0]
		provider.Headers = nil
		snapshot.HTTPProvider = &provider
	}
	if snapshot.CLIPreset != nil {
		preset := (videoconfig.Config{CLIPresets: []videoconfig.CLIPreset{*snapshot.CLIPreset}}).Clone().CLIPresets[0]
		preset.Env = nil
		snapshot.CLIPreset = &preset
	}
	return snapshot, nil
}

func validateResolvedTiming(timing ResolvedTiming) error {
	if (timing.InputMode != "duration" && timing.InputMode != "frames") || timing.FPS < 1 || timing.RequestedFrames < 1 || strings.TrimSpace(timing.AlgorithmVersion) == "" || timing.DurationSeconds < 0 {
		return fmt.Errorf("attempt snapshot timing is invalid")
	}
	return nil
}

func normalizeAttemptUpdate(input UpdateAttemptInput) (UpdateAttemptInput, error) {
	if !knownAttemptState(input.State) || input.State == AttemptInterrupted {
		return UpdateAttemptInput{}, fmt.Errorf("video attempt target state is invalid")
	}
	if input.QueuePosition < 0 || input.PID < 0 || input.ActualFrameCount < 0 {
		return UpdateAttemptInput{}, fmt.Errorf("video attempt position or counts are invalid")
	}
	if len(input.Error.Code) > 120 || len(input.Error.Message) > 4096 {
		return UpdateAttemptInput{}, fmt.Errorf("video attempt error exceeds size limit")
	}
	if input.OutputAssetID != "" && !validGeneratedID(input.OutputAssetID) {
		return UpdateAttemptInput{}, fmt.Errorf("video attempt result asset ID is invalid")
	}
	if filepath.IsAbs(input.WorkspaceRelativePath) || strings.Contains(input.WorkspaceRelativePath, "..") {
		return UpdateAttemptInput{}, fmt.Errorf("video attempt workspace path must be relative")
	}
	return input, nil
}

func validateDocument(document batchDocument, directoryID string) error {
	if document.SchemaVersion != batchSchemaVersion {
		return fmt.Errorf("schema version %d is unsupported", document.SchemaVersion)
	}
	batch := document.Batch
	if batch.ID != directoryID || !validGeneratedID(batch.ID) {
		return fmt.Errorf("batch identity is invalid")
	}
	if _, err := normalizeBatchInput(CreateBatchInput{Title: batch.Title, Folder: batch.Folder, ExecutionKind: batch.ExecutionKind, PresetID: batch.PresetID, Concurrency: batch.Concurrency, CommonParams: batch.CommonParams, Timing: batch.Timing}); err != nil {
		return err
	}
	if batch.CreatedAt.IsZero() || batch.UpdatedAt.IsZero() {
		return fmt.Errorf("batch timestamps are required")
	}
	seenItems := map[string]struct{}{}
	for i, item := range batch.Items {
		if !validGeneratedID(item.ID) || item.Order != i {
			return fmt.Errorf("item identity or order is invalid")
		}
		if _, exists := seenItems[item.ID]; exists {
			return fmt.Errorf("item identity is duplicated")
		}
		seenItems[item.ID] = struct{}{}
		if _, err := normalizeItemInput(CreateItemInput{Prompt: item.Prompt, NegativePrompt: item.NegativePrompt, Enabled: item.Enabled, ParamsOverride: item.ParamsOverride, TimingOverride: item.TimingOverride, InitImageID: item.InitImageID, EndImageID: item.EndImageID, ControlFrameIDs: item.ControlFrameIDs, SelectedAssets: item.SelectedAssets}); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("item timestamps are required")
		}
		active := 0
		seenAttempts := map[string]struct{}{}
		for _, attempt := range item.Attempts {
			if err := validateStoredAttempt(attempt, batch.ID, item.ID); err != nil {
				return err
			}
			if _, exists := seenAttempts[attempt.ID]; exists {
				return fmt.Errorf("attempt identity is duplicated")
			}
			seenAttempts[attempt.ID] = struct{}{}
			if activeAttemptState(attempt.State) {
				active++
			}
		}
		if active > 1 {
			return fmt.Errorf("item has multiple active attempts")
		}
	}
	return nil
}

func validateStoredAttempt(attempt Attempt, batchID, itemID string) error {
	if !validGeneratedID(attempt.ID) || attempt.BatchID != batchID || attempt.ItemID != itemID || !knownExecutionKind(attempt.ExecutionKind) || !knownAttemptState(attempt.State) || attempt.CreatedAt.IsZero() || attempt.Snapshot.CreatedAt.IsZero() {
		return fmt.Errorf("attempt identity, state, or timestamps are invalid")
	}
	if _, err := normalizeSnapshot(attempt.Snapshot); err != nil {
		return err
	}
	if attempt.Snapshot.ExecutionKind != attempt.ExecutionKind {
		return fmt.Errorf("attempt execution kind does not match snapshot")
	}
	if attempt.QueuePosition < 0 || attempt.PID < 0 || attempt.ActualFrameCount < 0 || len(attempt.Error.Code) > 120 || len(attempt.Error.Message) > 4096 || (attempt.OutputAssetID != "" && !validGeneratedID(attempt.OutputAssetID)) {
		return fmt.Errorf("attempt fields are invalid")
	}
	if filepath.IsAbs(attempt.WorkspaceRelativePath) || strings.Contains(attempt.WorkspaceRelativePath, "..") {
		return fmt.Errorf("attempt workspace path is invalid")
	}
	if attempt.State == AttemptQueued && attempt.StartedAt != nil {
		return fmt.Errorf("queued attempt cannot have a start time")
	}
	if (attempt.State == AttemptSubmitting || attempt.State == AttemptPolling || attempt.State == AttemptRunning) && attempt.StartedAt == nil {
		return fmt.Errorf("started attempt requires a start time")
	}
	if terminalAttemptState(attempt.State) != (attempt.CompletedAt != nil) {
		return fmt.Errorf("completion timestamp does not match state")
	}
	return nil
}

func canonicalBatch(batch Batch) Batch {
	batch.CommonParams, _ = normalizeParams(batch.CommonParams)
	for i := range batch.Items {
		batch.Items[i].ParamsOverride, _ = normalizeParams(batch.Items[i].ParamsOverride)
		batch.Items[i].TimingOverride = cloneTiming(batch.Items[i].TimingOverride)
		for j := range batch.Items[i].Attempts {
			snapshot, _ := normalizeSnapshot(batch.Items[i].Attempts[j].Snapshot)
			batch.Items[i].Attempts[j].Snapshot = snapshot
		}
	}
	return batch
}
func cloneBatch(batch Batch) Batch {
	batch.CommonParams = append(json.RawMessage(nil), batch.CommonParams...)
	batch.Items = append([]Item(nil), batch.Items...)
	for i := range batch.Items {
		batch.Items[i] = cloneItem(batch.Items[i])
	}
	return batch
}
func cloneItem(item Item) Item {
	item.ParamsOverride = append(json.RawMessage(nil), item.ParamsOverride...)
	item.TimingOverride = cloneTiming(item.TimingOverride)
	item.ControlFrameIDs = append([]string(nil), item.ControlFrameIDs...)
	item.SelectedAssets = append([]SelectedAsset(nil), item.SelectedAssets...)
	item.Attempts = append([]Attempt(nil), item.Attempts...)
	for i := range item.Attempts {
		item.Attempts[i] = cloneAttempt(item.Attempts[i])
	}
	return item
}
func cloneAttempt(attempt Attempt) Attempt {
	snapshot, _ := normalizeSnapshot(attempt.Snapshot)
	attempt.Snapshot = snapshot
	attempt.StartedAt = cloneTime(attempt.StartedAt)
	attempt.CompletedAt = cloneTime(attempt.CompletedAt)
	return attempt
}
func cloneTiming(timing *TimingInput) *TimingInput {
	if timing == nil {
		return nil
	}
	clone := *timing
	return &clone
}
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func interruptActiveAttempts(batch *Batch) bool {
	changed := false
	latest := batch.UpdatedAt
	for itemPos := range batch.Items {
		for attemptPos := range batch.Items[itemPos].Attempts {
			attempt := &batch.Items[itemPos].Attempts[attemptPos]
			if !activeAttemptState(attempt.State) {
				continue
			}
			now := nextTime(latest)
			attempt.State = AttemptInterrupted
			attempt.CompletedAt = &now
			attempt.Error = AttemptError{Code: "workbench_restarted", Message: "workbench restarted before the attempt completed"}
			batch.Items[itemPos].UpdatedAt = now
			latest, changed = now, true
		}
	}
	if changed {
		batch.UpdatedAt = latest
	}
	return changed
}
func activeAttemptState(state AttemptState) bool {
	return state == AttemptQueued || state == AttemptSubmitting || state == AttemptPolling || state == AttemptRunning
}
func terminalAttemptState(state AttemptState) bool {
	return state == AttemptSucceeded || state == AttemptFailed || state == AttemptCancelled || state == AttemptInterrupted
}
func knownAttemptState(state AttemptState) bool {
	return activeAttemptState(state) || terminalAttemptState(state)
}
func allowedAttemptTransition(from, to AttemptState) bool {
	switch from {
	case AttemptQueued:
		return to == AttemptSubmitting || to == AttemptRunning || to == AttemptCancelled
	case AttemptSubmitting:
		return to == AttemptPolling || to == AttemptRunning || to == AttemptFailed || to == AttemptCancelled
	case AttemptPolling:
		return to == AttemptPolling || to == AttemptRunning || to == AttemptSucceeded || to == AttemptFailed || to == AttemptCancelled
	case AttemptRunning:
		return to == AttemptRunning || to == AttemptSucceeded || to == AttemptFailed || to == AttemptCancelled
	default:
		return false
	}
}
func itemFromInput(id string, order int, input CreateItemInput, now time.Time) Item {
	return Item{ID: id, Order: order, Prompt: input.Prompt, NegativePrompt: input.NegativePrompt, Enabled: input.Enabled, ParamsOverride: input.ParamsOverride, TimingOverride: cloneTiming(input.TimingOverride), InitImageID: input.InitImageID, EndImageID: input.EndImageID, ControlFrameIDs: append([]string(nil), input.ControlFrameIDs...), SelectedAssets: append([]SelectedAsset(nil), input.SelectedAssets...), Attempts: []Attempt{}, CreatedAt: now, UpdatedAt: now}
}
func itemIndex(items []Item, id string) int {
	for i := range items {
		if items[i].ID == id {
			return i
		}
	}
	return -1
}
func attemptIndex(attempts []Attempt, id string) int {
	for i := range attempts {
		if attempts[i].ID == id {
			return i
		}
	}
	return -1
}

func (r *Repository) resultAssetAttached(assetID string) bool {
	for _, batch := range r.batches {
		for _, item := range batch.Items {
			for _, attempt := range item.Attempts {
				if attempt.OutputAssetID == assetID {
					return true
				}
			}
		}
	}
	return false
}
func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create video identifier: %w", err)
	}
	return hex.EncodeToString(value), nil
}
func validGeneratedID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}
func validPresetID(id string) bool {
	if len(id) < 1 || len(id) > 120 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
			return false
		}
	}
	return true
}
func knownExecutionKind(kind videoconfig.ExecutionKind) bool {
	return kind == videoconfig.ExecutionHTTP || kind == videoconfig.ExecutionLocalCLI
}
func nextTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}
