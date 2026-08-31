package videogen

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ekk1/ai-desktop/internal/asset"
)

const (
	videoItemReferenceModule    = "video_item"
	videoAttemptReferenceModule = "video_attempt"
	videoResultReferenceModule  = "video_result"
)

var (
	ErrVideoAssetNotFound  = errors.New("video asset not found")
	ErrVideoAssetNotActive = errors.New("video asset is not active")
	ErrVideoAssetType      = errors.New("video asset has an unsupported media type")
)

// Service makes video persistence and asset references one logical mutation.
// Callers should use it instead of Repository for every mutating video action.
type Service struct {
	mu         sync.RWMutex
	repository *Repository
	assets     *asset.Repository
}

type referenceChange struct {
	assetID   string
	reference asset.Reference
	add       bool
}

func NewService(repository *Repository, assets *asset.Repository) *Service {
	return &Service{repository: repository, assets: assets}
}

func (service *Service) List(filter Filter) []Batch {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.repository.List(filter)
}

func (service *Service) Get(batchID string) (Batch, bool) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.repository.Get(batchID)
}

func (service *Service) CreateBatch(input CreateBatchInput) (Batch, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.repository.CreateBatch(input)
}

func (service *Service) UpdateBatch(batchID string, input UpdateBatchInput) (Batch, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.repository.UpdateBatch(batchID, input)
}

func (service *Service) DeleteBatch(batchID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	before, ok := service.repository.Get(batchID)
	if !ok {
		return ErrBatchNotFound
	}
	for _, item := range before.Items {
		if hasActiveAttempt(item) {
			return ErrActiveAttempt
		}
	}
	completed, err := service.applyChanges(changesForBatch(before, false))
	if err != nil {
		return joinVideoServiceMutationError(err, service.compensate(completed))
	}
	if err := service.repository.DeleteBatch(batchID); err != nil {
		return joinVideoServiceMutationError(err, service.compensate(completed))
	}
	return nil
}

func (service *Service) CreateItems(batchID string, inputs []CreateItemInput) ([]Item, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, input := range inputs {
		if err := service.validateItemInputs(input, nil); err != nil {
			return nil, err
		}
	}
	before, ok := service.repository.Get(batchID)
	if !ok {
		return nil, ErrBatchNotFound
	}
	created, err := service.repository.CreateItems(batchID, inputs)
	if err != nil {
		return nil, err
	}
	changes := make([]referenceChange, 0)
	for _, item := range created {
		changes = append(changes, changesForItemInputs(item, true)...)
	}
	completed, err := service.applyChanges(changes)
	if err == nil {
		return created, nil
	}
	rollback := []error{service.repository.restoreBatch(before)}
	rollback = append(rollback, service.compensate(completed)...)
	return nil, joinVideoServiceMutationError(err, rollback)
}

func (service *Service) UpdateItem(batchID, itemID string, input UpdateItemInput) (Item, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	before, oldItem, err := service.batchItem(batchID, itemID)
	if err != nil {
		return Item{}, err
	}
	if err := service.validateItemInputs(input, videoItemInputSet(oldItem)); err != nil {
		return Item{}, err
	}
	updated, err := service.repository.UpdateItem(batchID, itemID, input)
	if err != nil {
		return Item{}, err
	}
	completed, err := service.applyChanges(itemInputReferenceDiff(oldItem, updated))
	if err == nil {
		return updated, nil
	}
	rollback := []error{service.repository.restoreBatch(before)}
	rollback = append(rollback, service.compensate(completed)...)
	return Item{}, joinVideoServiceMutationError(err, rollback)
}

func (service *Service) DeleteItem(batchID, itemID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	_, item, err := service.batchItem(batchID, itemID)
	if err != nil {
		return err
	}
	if hasActiveAttempt(item) {
		return ErrActiveAttempt
	}
	completed, err := service.applyChanges(changesForItem(item, false))
	if err != nil {
		return joinVideoServiceMutationError(err, service.compensate(completed))
	}
	if err := service.repository.DeleteItem(batchID, itemID); err != nil {
		return joinVideoServiceMutationError(err, service.compensate(completed))
	}
	return nil
}

func (service *Service) MoveItem(batchID, itemID string, offset int) (Batch, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.repository.MoveItem(batchID, itemID, offset)
}

func (service *Service) CreateAttempt(batchID, itemID string, input CreateAttemptInput) (Attempt, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	before, item, err := service.batchItem(batchID, itemID)
	if err != nil {
		return Attempt{}, err
	}
	if err := service.validateSnapshotInputs(&input.Snapshot, videoItemInputSet(item)); err != nil {
		return Attempt{}, err
	}
	created, err := service.repository.CreateAttempt(batchID, itemID, input)
	if err != nil {
		return Attempt{}, err
	}
	completed, err := service.applyChanges(changesForAttemptInputs(created, true))
	if err == nil {
		return created, nil
	}
	rollback := []error{service.repository.restoreBatch(before)}
	rollback = append(rollback, service.compensate(completed)...)
	return Attempt{}, joinVideoServiceMutationError(err, rollback)
}

func (service *Service) UpdateAttempt(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	_, item, err := service.batchItem(batchID, itemID)
	if err != nil {
		return Attempt{}, err
	}
	position := attemptIndex(item.Attempts, attemptID)
	if position < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	if input.OutputAssetID != "" && input.OutputAssetID != item.Attempts[position].OutputAssetID {
		return Attempt{}, fmt.Errorf("video attempt result must be attached first")
	}
	return service.repository.UpdateAttempt(batchID, itemID, attemptID, input)
}

// AttachVideoResult protects a generated video (or animated WebP) without
// requiring it to remain active in the asset browser.
func (service *Service) AttachVideoResult(batchID, itemID, attemptID, assetID string) (Attempt, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	item, err := service.videoResultAsset(assetID)
	if err != nil {
		return Attempt{}, err
	}
	_, parent, err := service.batchItem(batchID, itemID)
	if err != nil {
		return Attempt{}, err
	}
	position := attemptIndex(parent.Attempts, attemptID)
	if position < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	if parent.Attempts[position].OutputAssetID == assetID {
		return cloneAttempt(parent.Attempts[position]), nil
	}
	reference := asset.Reference{Module: videoResultReferenceModule, RecordID: attemptID}
	wasPresent := containsReference(item, reference)
	if _, err := service.assets.AddReference(assetID, reference); err != nil {
		return Attempt{}, fmt.Errorf("add video result reference: %w", err)
	}
	attached, err := service.repository.AttachResult(batchID, itemID, attemptID, assetID)
	if err == nil {
		return attached, nil
	}
	if wasPresent {
		return Attempt{}, err
	}
	_, rollbackErr := service.assets.RemoveReference(assetID, reference)
	return Attempt{}, joinVideoServiceMutationError(err, []error{rollbackErr})
}

func (service *Service) batchItem(batchID, itemID string) (Batch, Item, error) {
	batch, ok := service.repository.Get(batchID)
	if !ok {
		return Batch{}, Item{}, ErrBatchNotFound
	}
	position := itemIndex(batch.Items, itemID)
	if position < 0 {
		return Batch{}, Item{}, ErrItemNotFound
	}
	return batch, batch.Items[position], nil
}

func (service *Service) validateItemInputs(input CreateItemInput, retained map[string]struct{}) error {
	if err := validateSelectedAssetRoles(input.SelectedAssets); err != nil {
		return err
	}
	for _, candidate := range videoItemInputRequirements(input) {
		if _, err := service.validateInput(candidate.assetID, retained, candidate.requireImage); err != nil {
			return err
		}
	}
	return nil
}

func (service *Service) validateSnapshotInputs(snapshot *Snapshot, retained map[string]struct{}) error {
	for index := range snapshot.InputAssets {
		input := &snapshot.InputAssets[index]
		if !validVideoRole(input.Role) {
			return fmt.Errorf("video attempt input role is invalid")
		}
		item, err := service.validateInput(input.ID, retained, false)
		if err != nil {
			return err
		}
		input.SHA256, input.MediaType, input.DisplayName, input.Size, input.Order = item.SHA256, item.MediaType, item.DisplayName, item.Size, index
	}
	return nil
}

func (service *Service) validateInput(assetID string, retained map[string]struct{}, requireImage bool) (asset.Asset, error) {
	item, ok := service.assets.Get(assetID)
	if !ok {
		return asset.Asset{}, fmt.Errorf("%w: %s", ErrVideoAssetNotFound, assetID)
	}
	if requireImage && !strings.HasPrefix(strings.ToLower(item.MediaType), "image/") {
		return asset.Asset{}, fmt.Errorf("%w: %s", ErrVideoAssetType, assetID)
	}
	if _, alreadyReferenced := retained[assetID]; !alreadyReferenced && item.State != asset.StateActive {
		return asset.Asset{}, fmt.Errorf("%w: %s", ErrVideoAssetNotActive, assetID)
	}
	return item, nil
}

func (service *Service) videoResultAsset(assetID string) (asset.Asset, error) {
	item, ok := service.assets.Get(assetID)
	if !ok {
		return asset.Asset{}, fmt.Errorf("%w: %s", ErrVideoAssetNotFound, assetID)
	}
	mediaType := strings.ToLower(item.MediaType)
	if !strings.HasPrefix(mediaType, "video/") && mediaType != "image/webp" {
		return asset.Asset{}, fmt.Errorf("%w: %s", ErrVideoAssetType, assetID)
	}
	return item, nil
}

func (service *Service) applyChanges(changes []referenceChange) ([]referenceChange, error) {
	completed := make([]referenceChange, 0, len(changes))
	for _, change := range changes {
		var err error
		if change.add {
			_, err = service.assets.AddReference(change.assetID, change.reference)
		} else {
			_, err = service.assets.RemoveReference(change.assetID, change.reference)
		}
		if err != nil {
			return completed, fmt.Errorf("synchronize video asset reference: %w", err)
		}
		completed = append(completed, change)
	}
	return completed, nil
}

func (service *Service) compensate(completed []referenceChange) []error {
	errs := make([]error, 0)
	for index := len(completed) - 1; index >= 0; index-- {
		change := completed[index]
		var err error
		if change.add {
			_, err = service.assets.RemoveReference(change.assetID, change.reference)
		} else {
			_, err = service.assets.AddReference(change.assetID, change.reference)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("compensate video asset reference: %w", err))
		}
	}
	return errs
}

func (repository *Repository) restoreBatch(batch Batch) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := repository.save(batch); err != nil {
		return err
	}
	repository.batches[batch.ID] = cloneBatch(batch)
	return nil
}

type inputRequirement struct {
	assetID      string
	requireImage bool
}

func videoItemInputRequirements(item CreateItemInput) []inputRequirement {
	requirements := make([]inputRequirement, 0, 2+len(item.ControlFrameIDs)+len(item.SelectedAssets))
	requirements = append(requirements, inputRequirement{assetID: item.InitImageID, requireImage: true}, inputRequirement{assetID: item.EndImageID, requireImage: true})
	for _, id := range item.ControlFrameIDs {
		requirements = append(requirements, inputRequirement{assetID: id, requireImage: true})
	}
	for _, selected := range item.SelectedAssets {
		requirements = append(requirements, inputRequirement{assetID: selected.AssetID})
	}
	return uniqueInputRequirements(requirements)
}

func uniqueInputRequirements(requirements []inputRequirement) []inputRequirement {
	positions := make(map[string]int, len(requirements))
	unique := make([]inputRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		if requirement.assetID == "" {
			continue
		}
		if position, exists := positions[requirement.assetID]; exists {
			unique[position].requireImage = unique[position].requireImage || requirement.requireImage
			continue
		}
		positions[requirement.assetID] = len(unique)
		unique = append(unique, requirement)
	}
	return unique
}

func videoItemInputSet(item Item) map[string]struct{} {
	values := make(map[string]struct{})
	if item.InitImageID != "" {
		values[item.InitImageID] = struct{}{}
	}
	if item.EndImageID != "" {
		values[item.EndImageID] = struct{}{}
	}
	for _, id := range item.ControlFrameIDs {
		values[id] = struct{}{}
	}
	for _, selected := range item.SelectedAssets {
		values[selected.AssetID] = struct{}{}
	}
	return values
}

func itemInputReferenceDiff(previous, next Item) []referenceChange {
	oldSet, nextSet := videoItemInputSet(previous), videoItemInputSet(next)
	changes := make([]referenceChange, 0)
	for _, requirement := range videoItemInputRequirements(CreateItemInput{InitImageID: next.InitImageID, EndImageID: next.EndImageID, ControlFrameIDs: next.ControlFrameIDs, SelectedAssets: next.SelectedAssets}) {
		if _, present := oldSet[requirement.assetID]; !present {
			changes = append(changes, itemReferenceChange(requirement.assetID, next.ID, true))
		}
	}
	for _, id := range videoItemInputIDs(previous) {
		if _, present := nextSet[id]; !present {
			changes = append(changes, itemReferenceChange(id, previous.ID, false))
		}
	}
	return changes
}

func changesForBatch(batch Batch, add bool) []referenceChange {
	changes := make([]referenceChange, 0)
	for _, item := range batch.Items {
		changes = append(changes, changesForItem(item, add)...)
	}
	return changes
}

func changesForItem(item Item, add bool) []referenceChange {
	changes := changesForItemInputs(item, add)
	for _, attempt := range item.Attempts {
		changes = append(changes, changesForAttemptInputs(attempt, add)...)
		if attempt.OutputAssetID != "" {
			changes = append(changes, resultReferenceChange(attempt.OutputAssetID, attempt.ID, add))
		}
	}
	return changes
}

func changesForItemInputs(item Item, add bool) []referenceChange {
	changes := make([]referenceChange, 0)
	for _, id := range videoItemInputIDs(item) {
		changes = append(changes, itemReferenceChange(id, item.ID, add))
	}
	return changes
}

func changesForAttemptInputs(attempt Attempt, add bool) []referenceChange {
	changes := make([]referenceChange, 0)
	for _, snapshot := range attempt.Snapshot.InputAssets {
		changes = append(changes, referenceChange{assetID: snapshot.ID, reference: asset.Reference{Module: videoAttemptReferenceModule, RecordID: attempt.ID}, add: add})
	}
	return uniqueReferenceChanges(changes)
}

func videoItemInputIDs(item Item) []string {
	return inputRequirementIDs(videoItemInputRequirements(CreateItemInput{InitImageID: item.InitImageID, EndImageID: item.EndImageID, ControlFrameIDs: item.ControlFrameIDs, SelectedAssets: item.SelectedAssets}))
}

func inputRequirementIDs(requirements []inputRequirement) []string {
	ids := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		ids = append(ids, requirement.assetID)
	}
	return ids
}

func itemReferenceChange(assetID, itemID string, add bool) referenceChange {
	return referenceChange{assetID: assetID, reference: asset.Reference{Module: videoItemReferenceModule, RecordID: itemID}, add: add}
}

func resultReferenceChange(assetID, attemptID string, add bool) referenceChange {
	return referenceChange{assetID: assetID, reference: asset.Reference{Module: videoResultReferenceModule, RecordID: attemptID}, add: add}
}

func uniqueReferenceChanges(changes []referenceChange) []referenceChange {
	seen := make(map[referenceChange]struct{}, len(changes))
	unique := make([]referenceChange, 0, len(changes))
	for _, change := range changes {
		if _, exists := seen[change]; exists {
			continue
		}
		seen[change] = struct{}{}
		unique = append(unique, change)
	}
	return unique
}

func validateSelectedAssetRoles(selected []SelectedAsset) error {
	for _, item := range selected {
		if !validVideoRole(item.Role) {
			return fmt.Errorf("video selected asset role is invalid")
		}
	}
	return nil
}

func validVideoRole(value string) bool {
	if len(value) < 1 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("._-", character)) {
			return false
		}
	}
	return true
}

func hasActiveAttempt(item Item) bool {
	for _, attempt := range item.Attempts {
		if activeAttemptState(attempt.State) {
			return true
		}
	}
	return false
}

func containsReference(item asset.Asset, reference asset.Reference) bool {
	for _, existing := range item.References {
		if existing == reference {
			return true
		}
	}
	return false
}

func joinVideoServiceMutationError(primary error, rollback []error) error {
	all := []error{primary}
	for _, err := range rollback {
		if err != nil {
			all = append(all, fmt.Errorf("rollback: %w", err))
		}
	}
	return errors.Join(all...)
}
