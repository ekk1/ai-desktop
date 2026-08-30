package imagegen

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

	"github.com/ekk1/ai-desktop/internal/sdcpp"
	"github.com/ekk1/ai-desktop/internal/store"
)

var (
	ErrBatchNotFound            = errors.New("image batch not found")
	ErrItemNotFound             = errors.New("image item not found")
	ErrAttemptNotFound          = errors.New("image attempt not found")
	ErrMoveBoundary             = errors.New("image item move exceeds batch boundary")
	ErrActiveAttempt            = errors.New("image item already has an active attempt")
	ErrInvalidAttemptTransition = errors.New("invalid image attempt state transition")
)

var managedImageParams = map[string]struct{}{
	"prompt": {}, "negative_prompt": {}, "init_image": {}, "ref_images": {},
	"mask_image": {}, "control_image": {}, "ip_adapter_image": {},
}

type Repository struct {
	mu      sync.RWMutex
	root    string
	batches map[string]Batch
}

func OpenRepository(root string) (*Repository, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create image batch directory: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read image batch directory: %w", err)
	}
	repository := &Repository{root: root, batches: make(map[string]Batch)}
	for _, entry := range entries {
		if !entry.IsDir() || !validGeneratedID(entry.Name()) {
			continue
		}
		var document batchDocument
		if err := store.ReadJSON(filepath.Join(root, entry.Name(), "batch.json"), &document); err != nil {
			return nil, fmt.Errorf("load image batch %q: %w", entry.Name(), err)
		}
		if err := validateDocument(document, entry.Name()); err != nil {
			return nil, fmt.Errorf("validate image batch %q: %w", entry.Name(), err)
		}
		document.Batch = canonicalBatch(document.Batch)
		if interruptActiveAttempts(&document.Batch) {
			if err := repository.save(document.Batch); err != nil {
				return nil, fmt.Errorf("recover image batch %q: %w", entry.Name(), err)
			}
		}
		repository.batches[document.Batch.ID] = cloneBatch(document.Batch)
	}
	return repository, nil
}

func (repository *Repository) CreateBatch(input CreateBatchInput) (Batch, error) {
	normalized, err := normalizeBatchInput(input)
	if err != nil {
		return Batch{}, err
	}
	id, err := randomID()
	if err != nil {
		return Batch{}, err
	}
	now := time.Now().UTC()
	created := Batch{
		ID: id, Title: normalized.Title, Folder: normalized.Folder, ProviderID: normalized.ProviderID,
		Concurrency: normalized.Concurrency, BaseParams: normalized.BaseParams, Items: []Item{},
		CreatedAt: now, UpdatedAt: now,
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := repository.save(created); err != nil {
		return Batch{}, err
	}
	repository.batches[id] = cloneBatch(created)
	return cloneBatch(created), nil
}

func (repository *Repository) List(filter Filter) []Batch {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	result := make([]Batch, 0, len(repository.batches))
	for _, batch := range repository.batches {
		if filter.Folder != "" && batch.Folder != filter.Folder {
			continue
		}
		if filter.ProviderID != "" && batch.ProviderID != filter.ProviderID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(batch.Folder+"\n"+batch.Title), query) {
			continue
		}
		result = append(result, cloneBatch(batch))
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result
}

func (repository *Repository) Get(batchID string) (Batch, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	batch, ok := repository.batches[batchID]
	return cloneBatch(batch), ok
}

func (repository *Repository) UpdateBatch(batchID string, input UpdateBatchInput) (Batch, error) {
	normalized, err := normalizeBatchInput(input)
	if err != nil {
		return Batch{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Batch{}, ErrBatchNotFound
	}
	updated := cloneBatch(batch)
	updated.Title, updated.Folder, updated.ProviderID = normalized.Title, normalized.Folder, normalized.ProviderID
	updated.Concurrency, updated.BaseParams = normalized.Concurrency, normalized.BaseParams
	updated.UpdatedAt = nextTime(batch.UpdatedAt)
	if err := repository.save(updated); err != nil {
		return Batch{}, err
	}
	repository.batches[batchID] = updated
	return cloneBatch(updated), nil
}

func (repository *Repository) DeleteBatch(batchID string) error {
	if !validGeneratedID(batchID) {
		return ErrBatchNotFound
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, ok := repository.batches[batchID]; !ok {
		return ErrBatchNotFound
	}
	if err := os.RemoveAll(filepath.Join(repository.root, batchID)); err != nil {
		return fmt.Errorf("delete image batch: %w", err)
	}
	delete(repository.batches, batchID)
	return nil
}

func (repository *Repository) CreateItems(batchID string, inputs []CreateItemInput) ([]Item, error) {
	if len(inputs) == 0 {
		return nil, fmt.Errorf("at least one image item is required")
	}
	normalized := make([]CreateItemInput, len(inputs))
	for index, input := range inputs {
		var err error
		normalized[index], err = normalizeItemInput(input)
		if err != nil {
			return nil, fmt.Errorf("image item %d: %w", index, err)
		}
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
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
		item := Item{
			ID: id, Order: len(updated.Items), Prompt: input.Prompt, NegativePrompt: input.NegativePrompt,
			ParamsOverride: input.ParamsOverride, InputAssets: input.InputAssets, Attempts: []Attempt{},
			CreatedAt: now, UpdatedAt: now,
		}
		updated.Items = append(updated.Items, item)
		created = append(created, cloneItem(item))
		updated.UpdatedAt = now
	}
	if err := repository.save(updated); err != nil {
		return nil, err
	}
	repository.batches[batchID] = updated
	return created, nil
}

func (repository *Repository) UpdateItem(batchID, itemID string, input UpdateItemInput) (Item, error) {
	normalized, err := normalizeItemInput(input)
	if err != nil {
		return Item{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Item{}, ErrBatchNotFound
	}
	index := itemIndex(batch.Items, itemID)
	if index < 0 {
		return Item{}, ErrItemNotFound
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	item := &updated.Items[index]
	item.Prompt, item.NegativePrompt = normalized.Prompt, normalized.NegativePrompt
	item.ParamsOverride, item.InputAssets = normalized.ParamsOverride, normalized.InputAssets
	item.UpdatedAt, updated.UpdatedAt = now, now
	if err := repository.save(updated); err != nil {
		return Item{}, err
	}
	repository.batches[batchID] = updated
	return cloneItem(*item), nil
}

func (repository *Repository) DeleteItem(batchID, itemID string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return ErrBatchNotFound
	}
	index := itemIndex(batch.Items, itemID)
	if index < 0 {
		return ErrItemNotFound
	}
	updated := cloneBatch(batch)
	updated.Items = append(updated.Items[:index], updated.Items[index+1:]...)
	for itemIndex := range updated.Items {
		updated.Items[itemIndex].Order = itemIndex
	}
	updated.UpdatedAt = nextTime(batch.UpdatedAt)
	if err := repository.save(updated); err != nil {
		return err
	}
	repository.batches[batchID] = updated
	return nil
}

func (repository *Repository) MoveItem(batchID, itemID string, offset int) (Batch, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Batch{}, ErrBatchNotFound
	}
	index := itemIndex(batch.Items, itemID)
	if index < 0 {
		return Batch{}, ErrItemNotFound
	}
	target := index + offset
	if offset == 0 || target < 0 || target >= len(batch.Items) {
		return Batch{}, ErrMoveBoundary
	}
	updated := cloneBatch(batch)
	moving := updated.Items[index]
	if target < index {
		copy(updated.Items[target+1:index+1], updated.Items[target:index])
	} else {
		copy(updated.Items[index:target], updated.Items[index+1:target+1])
	}
	updated.Items[target] = moving
	for itemIndex := range updated.Items {
		updated.Items[itemIndex].Order = itemIndex
	}
	updated.UpdatedAt = nextTime(batch.UpdatedAt)
	if err := repository.save(updated); err != nil {
		return Batch{}, err
	}
	repository.batches[batchID] = updated
	return cloneBatch(updated), nil
}

func (repository *Repository) CreateAttempt(batchID, itemID string, input CreateAttemptInput) (Attempt, error) {
	if input.State != AttemptQueued {
		return Attempt{}, fmt.Errorf("new image attempt state must be %q", AttemptQueued)
	}
	snapshot, err := normalizeSnapshot(input.Snapshot)
	if err != nil {
		return Attempt{}, err
	}
	id, err := randomID()
	if err != nil {
		return Attempt{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	index := itemIndex(batch.Items, itemID)
	if index < 0 {
		return Attempt{}, ErrItemNotFound
	}
	for _, attempt := range batch.Items[index].Attempts {
		if activeAttemptState(attempt.State) {
			return Attempt{}, ErrActiveAttempt
		}
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	snapshot.CreatedAt = now
	created := Attempt{
		ID: id, State: AttemptQueued, Snapshot: snapshot, ResultAssetIDs: []string{},
		CreatedAt: now,
	}
	updated.Items[index].Attempts = append(updated.Items[index].Attempts, created)
	updated.Items[index].UpdatedAt, updated.UpdatedAt = now, now
	if err := repository.save(updated); err != nil {
		return Attempt{}, err
	}
	repository.batches[batchID] = updated
	return cloneAttempt(created), nil
}

func (repository *Repository) createFailedAttempt(batchID, itemID string, snapshot Snapshot, failure AttemptError) (Attempt, error) {
	normalized, err := normalizeSnapshot(snapshot)
	if err != nil {
		return Attempt{}, err
	}
	if len(failure.Message) > 4096 {
		return Attempt{}, fmt.Errorf("image attempt error message exceeds 4096 bytes")
	}
	id, err := randomID()
	if err != nil {
		return Attempt{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	position := itemIndex(batch.Items, itemID)
	if position < 0 {
		return Attempt{}, ErrItemNotFound
	}
	for _, attempt := range batch.Items[position].Attempts {
		if activeAttemptState(attempt.State) {
			return Attempt{}, ErrActiveAttempt
		}
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	normalized.CreatedAt = now
	created := Attempt{
		ID: id, State: AttemptFailed, Snapshot: normalized, ResultAssetIDs: []string{}, Error: failure,
		CreatedAt: now, CompletedAt: now,
	}
	updated.Items[position].Attempts = append(updated.Items[position].Attempts, created)
	updated.Items[position].UpdatedAt, updated.UpdatedAt = now, now
	if err := repository.save(updated); err != nil {
		return Attempt{}, err
	}
	repository.batches[batchID] = updated
	return cloneAttempt(created), nil
}

func (repository *Repository) UpdateAttempt(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
	normalized, err := normalizeAttemptUpdate(input)
	if err != nil {
		return Attempt{}, err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	itemPosition := itemIndex(batch.Items, itemID)
	if itemPosition < 0 {
		return Attempt{}, ErrItemNotFound
	}
	attemptPosition := attemptIndex(batch.Items[itemPosition].Attempts, attemptID)
	if attemptPosition < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	current := batch.Items[itemPosition].Attempts[attemptPosition]
	if !allowedAttemptTransition(current.State, normalized.State) {
		return Attempt{}, ErrInvalidAttemptTransition
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	attempt := &updated.Items[itemPosition].Attempts[attemptPosition]
	attempt.State = normalized.State
	attempt.RemoteJobID = normalized.RemoteJobID
	attempt.RemoteStatus = normalized.RemoteStatus
	attempt.QueuePosition = normalized.QueuePosition
	attempt.ResultAssetIDs = normalized.ResultAssetIDs
	attempt.Error = normalized.Error
	if normalized.State == AttemptSubmitting && attempt.StartedAt.IsZero() {
		attempt.StartedAt = now
	}
	if terminalAttemptState(normalized.State) {
		attempt.CompletedAt = now
	}
	updated.Items[itemPosition].UpdatedAt, updated.UpdatedAt = now, now
	if err := repository.save(updated); err != nil {
		return Attempt{}, err
	}
	repository.batches[batchID] = updated
	return cloneAttempt(*attempt), nil
}

func (repository *Repository) attachAttemptResult(batchID, itemID, attemptID, assetID string) (Attempt, error) {
	if !validGeneratedID(assetID) {
		return Attempt{}, fmt.Errorf("image attempt result asset ID is invalid")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	batch, ok := repository.batches[batchID]
	if !ok {
		return Attempt{}, ErrBatchNotFound
	}
	itemPosition := itemIndex(batch.Items, itemID)
	if itemPosition < 0 {
		return Attempt{}, ErrItemNotFound
	}
	attemptPosition := attemptIndex(batch.Items[itemPosition].Attempts, attemptID)
	if attemptPosition < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	current := batch.Items[itemPosition].Attempts[attemptPosition]
	for _, existing := range current.ResultAssetIDs {
		if existing == assetID {
			return cloneAttempt(current), nil
		}
	}
	updated := cloneBatch(batch)
	now := nextTime(batch.UpdatedAt)
	attempt := &updated.Items[itemPosition].Attempts[attemptPosition]
	attempt.ResultAssetIDs = append(attempt.ResultAssetIDs, assetID)
	updated.Items[itemPosition].UpdatedAt, updated.UpdatedAt = now, now
	if err := repository.save(updated); err != nil {
		return Attempt{}, err
	}
	repository.batches[batchID] = updated
	return cloneAttempt(*attempt), nil
}

func (repository *Repository) save(batch Batch) error {
	document := batchDocument{SchemaVersion: batchSchemaVersion, Batch: cloneBatch(batch)}
	return store.WriteJSON(filepath.Join(repository.root, batch.ID, "batch.json"), document, 0o600)
}

func (repository *Repository) restoreBatch(batch Batch) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if !validGeneratedID(batch.ID) {
		return fmt.Errorf("restore image batch identity is invalid")
	}
	if err := repository.save(batch); err != nil {
		return err
	}
	repository.batches[batch.ID] = cloneBatch(batch)
	return nil
}

func normalizeBatchInput(input CreateBatchInput) (CreateBatchInput, error) {
	input.Title = strings.TrimSpace(input.Title)
	input.Folder = strings.TrimSpace(input.Folder)
	input.ProviderID = strings.TrimSpace(input.ProviderID)
	if input.Title == "" {
		return CreateBatchInput{}, fmt.Errorf("image batch title is required")
	}
	if input.ProviderID == "" {
		return CreateBatchInput{}, fmt.Errorf("image provider ID is required")
	}
	if input.Concurrency < 1 || input.Concurrency > 16 {
		return CreateBatchInput{}, fmt.Errorf("image batch concurrency must be between 1 and 16")
	}
	params, err := normalizeParams(input.BaseParams)
	if err != nil {
		return CreateBatchInput{}, fmt.Errorf("base params: %w", err)
	}
	input.BaseParams = params
	return input, nil
}

func normalizeItemInput(input CreateItemInput) (CreateItemInput, error) {
	params, err := normalizeParams(input.ParamsOverride)
	if err != nil {
		return CreateItemInput{}, fmt.Errorf("params override: %w", err)
	}
	assets, err := normalizeInputAssets(input.InputAssets)
	if err != nil {
		return CreateItemInput{}, err
	}
	input.ParamsOverride, input.InputAssets = params, assets
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
		if _, managed := managedImageParams[key]; managed {
			return nil, fmt.Errorf("%q is managed by the workbench", key)
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return nil, fmt.Errorf("must be one JSON Object")
	}
	return append(json.RawMessage(nil), compact.Bytes()...), nil
}

func normalizeInputAssets(input InputAssets) (InputAssets, error) {
	for name, id := range map[string]string{
		"init image": input.InitImageID, "mask image": input.MaskImageID,
		"control image": input.ControlImageID, "IP adapter image": input.IPAdapterImageID,
	} {
		if id != "" && !validGeneratedID(id) {
			return InputAssets{}, fmt.Errorf("%s ID is invalid", name)
		}
	}
	seen := make(map[string]struct{}, len(input.RefImageIDs))
	refs := make([]string, 0, len(input.RefImageIDs))
	for _, id := range input.RefImageIDs {
		if !validGeneratedID(id) {
			return InputAssets{}, fmt.Errorf("reference image ID is invalid")
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			refs = append(refs, id)
		}
	}
	input.RefImageIDs = refs
	return input, nil
}

func normalizeSnapshot(snapshot Snapshot) (Snapshot, error) {
	if err := (sdcpp.ImageConfig{Providers: []sdcpp.ImageProvider{snapshot.Provider}}).Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("attempt snapshot provider: %w", err)
	}
	params, err := normalizeParams(snapshot.Params)
	if err != nil {
		return Snapshot{}, fmt.Errorf("attempt snapshot params: %w", err)
	}
	for index, item := range snapshot.InputAssets {
		if !validGeneratedID(item.ID) {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d ID is invalid", index)
		}
		if len(item.SHA256) != 64 {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d SHA-256 is invalid", index)
		}
		if _, err := hex.DecodeString(item.SHA256); err != nil {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d SHA-256 is invalid", index)
		}
		if strings.TrimSpace(item.MediaType) == "" || strings.TrimSpace(item.DisplayName) == "" {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d metadata is required", index)
		}
		if item.Size < 0 || item.Width < 0 || item.Height < 0 {
			return Snapshot{}, fmt.Errorf("attempt snapshot asset %d dimensions or size are invalid", index)
		}
	}
	snapshot.Provider = cloneProvider(snapshot.Provider)
	snapshot.Params = params
	snapshot.InputAssets = append([]AssetSnapshot(nil), snapshot.InputAssets...)
	return snapshot, nil
}

func normalizeAttemptUpdate(input UpdateAttemptInput) (UpdateAttemptInput, error) {
	if !knownAttemptState(input.State) || input.State == AttemptInterrupted {
		return UpdateAttemptInput{}, fmt.Errorf("image attempt target state is invalid")
	}
	if input.QueuePosition < 0 {
		return UpdateAttemptInput{}, fmt.Errorf("image attempt queue position cannot be negative")
	}
	if len(input.Error.Message) > 4096 {
		return UpdateAttemptInput{}, fmt.Errorf("image attempt error message exceeds 4096 bytes")
	}
	resultIDs := make([]string, 0, len(input.ResultAssetIDs))
	seen := make(map[string]struct{}, len(input.ResultAssetIDs))
	for _, id := range input.ResultAssetIDs {
		if !validGeneratedID(id) {
			return UpdateAttemptInput{}, fmt.Errorf("image attempt result asset ID is invalid")
		}
		if _, duplicate := seen[id]; !duplicate {
			seen[id] = struct{}{}
			resultIDs = append(resultIDs, id)
		}
	}
	input.ResultAssetIDs = resultIDs
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
	if _, err := normalizeBatchInput(CreateBatchInput{Title: batch.Title, Folder: batch.Folder, ProviderID: batch.ProviderID, Concurrency: batch.Concurrency, BaseParams: batch.BaseParams}); err != nil {
		return err
	}
	if batch.CreatedAt.IsZero() || batch.UpdatedAt.IsZero() {
		return fmt.Errorf("batch timestamps are required")
	}
	seen := make(map[string]struct{}, len(batch.Items))
	for index, item := range batch.Items {
		if !validGeneratedID(item.ID) {
			return fmt.Errorf("item %d identity is invalid", index)
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return fmt.Errorf("item %d identity is duplicated", index)
		}
		seen[item.ID] = struct{}{}
		if item.Order != index {
			return fmt.Errorf("item %q order is invalid", item.ID)
		}
		if _, err := normalizeItemInput(CreateItemInput{Prompt: item.Prompt, NegativePrompt: item.NegativePrompt, ParamsOverride: item.ParamsOverride, InputAssets: item.InputAssets}); err != nil {
			return fmt.Errorf("item %q: %w", item.ID, err)
		}
		if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("item %q timestamps are required", item.ID)
		}
		attemptIDs := make(map[string]struct{}, len(item.Attempts))
		activeAttempts := 0
		for attemptPosition, attempt := range item.Attempts {
			if err := validateStoredAttempt(attempt); err != nil {
				return fmt.Errorf("item %q attempt %d: %w", item.ID, attemptPosition, err)
			}
			if _, duplicate := attemptIDs[attempt.ID]; duplicate {
				return fmt.Errorf("item %q attempt identity is duplicated", item.ID)
			}
			attemptIDs[attempt.ID] = struct{}{}
			if activeAttemptState(attempt.State) {
				activeAttempts++
			}
		}
		if activeAttempts > 1 {
			return fmt.Errorf("item %q has multiple active attempts", item.ID)
		}
	}
	return nil
}

func cloneBatch(batch Batch) Batch {
	batch.BaseParams = append(json.RawMessage(nil), batch.BaseParams...)
	items := batch.Items
	batch.Items = make([]Item, len(items))
	for index, item := range items {
		batch.Items[index] = cloneItem(item)
	}
	return batch
}

func canonicalBatch(batch Batch) Batch {
	batch.BaseParams, _ = normalizeParams(batch.BaseParams)
	for index := range batch.Items {
		batch.Items[index].ParamsOverride, _ = normalizeParams(batch.Items[index].ParamsOverride)
		for attemptIndex := range batch.Items[index].Attempts {
			batch.Items[index].Attempts[attemptIndex].Snapshot.Params, _ = normalizeParams(batch.Items[index].Attempts[attemptIndex].Snapshot.Params)
		}
	}
	return batch
}

func cloneItem(item Item) Item {
	item.ParamsOverride = append(json.RawMessage(nil), item.ParamsOverride...)
	item.InputAssets.RefImageIDs = append([]string(nil), item.InputAssets.RefImageIDs...)
	attempts := item.Attempts
	item.Attempts = make([]Attempt, len(attempts))
	for index, attempt := range attempts {
		item.Attempts[index] = cloneAttempt(attempt)
	}
	return item
}

func cloneAttempt(attempt Attempt) Attempt {
	attempt.Snapshot.Provider = cloneProvider(attempt.Snapshot.Provider)
	attempt.Snapshot.Params = append(json.RawMessage(nil), attempt.Snapshot.Params...)
	attempt.Snapshot.InputAssets = append([]AssetSnapshot(nil), attempt.Snapshot.InputAssets...)
	attempt.ResultAssetIDs = append([]string(nil), attempt.ResultAssetIDs...)
	return attempt
}

func cloneProvider(provider sdcpp.ImageProvider) sdcpp.ImageProvider {
	headers := make(map[string]string, len(provider.Headers))
	for key, value := range provider.Headers {
		headers[key] = value
	}
	provider.Headers = headers
	return provider
}

func validateStoredAttempt(attempt Attempt) error {
	if !validGeneratedID(attempt.ID) || !knownAttemptState(attempt.State) {
		return fmt.Errorf("identity or state is invalid")
	}
	if attempt.CreatedAt.IsZero() || attempt.Snapshot.CreatedAt.IsZero() {
		return fmt.Errorf("created timestamps are required")
	}
	if _, err := normalizeSnapshot(attempt.Snapshot); err != nil {
		return err
	}
	if attempt.QueuePosition < 0 || len(attempt.Error.Message) > 4096 {
		return fmt.Errorf("queue position or error message is invalid")
	}
	seen := make(map[string]struct{}, len(attempt.ResultAssetIDs))
	for _, id := range attempt.ResultAssetIDs {
		if !validGeneratedID(id) {
			return fmt.Errorf("result asset identity is invalid")
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("result asset identity is duplicated")
		}
		seen[id] = struct{}{}
	}
	if attempt.State == AttemptQueued && !attempt.StartedAt.IsZero() {
		return fmt.Errorf("queued attempt cannot have a start time")
	}
	if (attempt.State == AttemptSubmitting || attempt.State == AttemptPolling) && attempt.StartedAt.IsZero() {
		return fmt.Errorf("started attempt requires a start time")
	}
	if terminalAttemptState(attempt.State) != !attempt.CompletedAt.IsZero() {
		return fmt.Errorf("completion timestamp does not match state")
	}
	return nil
}

func interruptActiveAttempts(batch *Batch) bool {
	changed := false
	latest := batch.UpdatedAt
	for itemIndex := range batch.Items {
		for attemptIndex := range batch.Items[itemIndex].Attempts {
			attempt := &batch.Items[itemIndex].Attempts[attemptIndex]
			if !activeAttemptState(attempt.State) {
				continue
			}
			now := nextTime(latest)
			attempt.State = AttemptInterrupted
			attempt.CompletedAt = now
			attempt.Error = AttemptError{Code: "workbench_restarted", Message: "workbench restarted before the attempt completed"}
			batch.Items[itemIndex].UpdatedAt = now
			latest = now
			changed = true
		}
	}
	if changed {
		batch.UpdatedAt = latest
	}
	return changed
}

func activeAttemptState(state AttemptState) bool {
	return state == AttemptQueued || state == AttemptSubmitting || state == AttemptPolling
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
		return to == AttemptSubmitting || to == AttemptCancelled
	case AttemptSubmitting:
		return to == AttemptPolling || to == AttemptFailed || to == AttemptCancelled
	case AttemptPolling:
		return to == AttemptPolling || to == AttemptSucceeded || to == AttemptFailed || to == AttemptCancelled
	default:
		return false
	}
}

func attemptIndex(attempts []Attempt, attemptID string) int {
	for index := range attempts {
		if attempts[index].ID == attemptID {
			return index
		}
	}
	return -1
}

func itemIndex(items []Item, itemID string) int {
	for index := range items {
		if items[index].ID == itemID {
			return index
		}
	}
	return -1
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create image identifier: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func validGeneratedID(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func nextTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}
