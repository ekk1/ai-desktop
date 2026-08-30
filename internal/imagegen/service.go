package imagegen

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/sdcpp"
)

const (
	imageItemReferenceModule    = "image_item"
	imageAttemptReferenceModule = "image_attempt"
)

var (
	ErrImageAssetNotFound = errors.New("image asset not found")
	ErrImageAssetType     = errors.New("asset is not an image")
)

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
	if len(input.BaseParams) == 0 {
		input.BaseParams = sdcpp.DefaultImageParams()
	}
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
	changes := changesForBatch(before, false)
	completed, err := service.applyChanges(changes)
	if err != nil {
		return joinServiceMutationError(err, service.compensate(completed))
	}
	if err := service.repository.DeleteBatch(batchID); err != nil {
		return joinServiceMutationError(err, service.compensate(completed))
	}
	return nil
}

func (service *Service) CreateItems(batchID string, inputs []CreateItemInput) ([]Item, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	for _, input := range inputs {
		if err := service.validateImageAssets(inputAssetIDs(input.InputAssets)); err != nil {
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
	return nil, joinServiceMutationError(err, rollback)
}

func (service *Service) UpdateItem(batchID, itemID string, input UpdateItemInput) (Item, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.validateImageAssets(inputAssetIDs(input.InputAssets)); err != nil {
		return Item{}, err
	}
	before, oldItem, err := service.batchItem(batchID, itemID)
	if err != nil {
		return Item{}, err
	}
	updated, err := service.repository.UpdateItem(batchID, itemID, input)
	if err != nil {
		return Item{}, err
	}
	changes := inputReferenceDiff(oldItem, updated)
	completed, err := service.applyChanges(changes)
	if err == nil {
		return updated, nil
	}
	rollback := []error{service.repository.restoreBatch(before)}
	rollback = append(rollback, service.compensate(completed)...)
	return Item{}, joinServiceMutationError(err, rollback)
}

func (service *Service) DeleteItem(batchID, itemID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	_, item, err := service.batchItem(batchID, itemID)
	if err != nil {
		return err
	}
	changes := changesForItem(item, false)
	completed, err := service.applyChanges(changes)
	if err != nil {
		return joinServiceMutationError(err, service.compensate(completed))
	}
	if err := service.repository.DeleteItem(batchID, itemID); err != nil {
		return joinServiceMutationError(err, service.compensate(completed))
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
	return service.repository.CreateAttempt(batchID, itemID, input)
}

func (service *Service) UpdateAttempt(batchID, itemID, attemptID string, input UpdateAttemptInput) (Attempt, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.repository.UpdateAttempt(batchID, itemID, attemptID, input)
}

func (service *Service) AttachResult(batchID, itemID, attemptID, assetID string) (Attempt, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.validateImageAssets([]string{assetID}); err != nil {
		return Attempt{}, err
	}
	_, item, err := service.batchItem(batchID, itemID)
	if err != nil {
		return Attempt{}, err
	}
	attemptPosition := attemptIndex(item.Attempts, attemptID)
	if attemptPosition < 0 {
		return Attempt{}, ErrAttemptNotFound
	}
	for _, existing := range item.Attempts[attemptPosition].ResultAssetIDs {
		if existing == assetID {
			return cloneAttempt(item.Attempts[attemptPosition]), nil
		}
	}
	reference := asset.Reference{Module: imageAttemptReferenceModule, RecordID: attemptID}
	assetBefore, _ := service.assets.Get(assetID)
	referenceWasPresent := containsAssetReference(assetBefore, reference)
	if _, err := service.assets.AddReference(assetID, reference); err != nil {
		return Attempt{}, fmt.Errorf("add image result reference: %w", err)
	}
	attached, err := service.repository.attachAttemptResult(batchID, itemID, attemptID, assetID)
	if err == nil {
		return attached, nil
	}
	if referenceWasPresent {
		return Attempt{}, err
	}
	_, rollbackErr := service.assets.RemoveReference(assetID, reference)
	return Attempt{}, joinServiceMutationError(err, []error{rollbackErr})
}

func (service *Service) batchItem(batchID, itemID string) (Batch, Item, error) {
	batch, ok := service.repository.Get(batchID)
	if !ok {
		return Batch{}, Item{}, ErrBatchNotFound
	}
	index := itemIndex(batch.Items, itemID)
	if index < 0 {
		return Batch{}, Item{}, ErrItemNotFound
	}
	return batch, batch.Items[index], nil
}

func (service *Service) validateImageAssets(ids []string) error {
	for _, id := range ids {
		item, ok := service.assets.Get(id)
		if !ok {
			return fmt.Errorf("%w: %s", ErrImageAssetNotFound, id)
		}
		if !strings.HasPrefix(strings.ToLower(item.MediaType), "image/") {
			return fmt.Errorf("%w: %s", ErrImageAssetType, id)
		}
	}
	return nil
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
			return completed, fmt.Errorf("synchronize image asset reference: %w", err)
		}
		completed = append(completed, change)
	}
	return completed, nil
}

func (service *Service) compensate(completed []referenceChange) []error {
	found := make([]error, 0)
	for index := len(completed) - 1; index >= 0; index-- {
		change := completed[index]
		var err error
		if change.add {
			_, err = service.assets.RemoveReference(change.assetID, change.reference)
		} else {
			_, err = service.assets.AddReference(change.assetID, change.reference)
		}
		if err != nil {
			found = append(found, fmt.Errorf("compensate image asset reference: %w", err))
		}
	}
	return found
}

func inputReferenceDiff(previous, next Item) []referenceChange {
	oldIDs := inputAssetIDs(previous.InputAssets)
	newIDs := inputAssetIDs(next.InputAssets)
	oldSet, newSet := stringSet(oldIDs), stringSet(newIDs)
	changes := make([]referenceChange, 0)
	for _, id := range newIDs {
		if _, present := oldSet[id]; !present {
			changes = append(changes, itemReferenceChange(id, next.ID, true))
		}
	}
	for _, id := range oldIDs {
		if _, present := newSet[id]; !present {
			changes = append(changes, itemReferenceChange(id, next.ID, false))
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
		for _, id := range uniqueStrings(attempt.ResultAssetIDs) {
			changes = append(changes, referenceChange{
				assetID: id, add: add,
				reference: asset.Reference{Module: imageAttemptReferenceModule, RecordID: attempt.ID},
			})
		}
	}
	return changes
}

func changesForItemInputs(item Item, add bool) []referenceChange {
	ids := inputAssetIDs(item.InputAssets)
	changes := make([]referenceChange, 0, len(ids))
	for _, id := range ids {
		changes = append(changes, itemReferenceChange(id, item.ID, add))
	}
	return changes
}

func itemReferenceChange(assetID, itemID string, add bool) referenceChange {
	return referenceChange{
		assetID: assetID, add: add,
		reference: asset.Reference{Module: imageItemReferenceModule, RecordID: itemID},
	}
}

func inputAssetIDs(input InputAssets) []string {
	candidates := make([]string, 0, 4+len(input.RefImageIDs))
	candidates = append(candidates, input.InitImageID)
	candidates = append(candidates, input.RefImageIDs...)
	candidates = append(candidates, input.MaskImageID, input.ControlImageID, input.IPAdapterImageID)
	return uniqueStrings(candidates)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
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

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func joinServiceMutationError(primary error, rollback []error) error {
	all := []error{primary}
	for _, err := range rollback {
		if err != nil {
			all = append(all, fmt.Errorf("rollback: %w", err))
		}
	}
	return errors.Join(all...)
}

func containsAssetReference(item asset.Asset, reference asset.Reference) bool {
	for _, existing := range item.References {
		if existing == reference {
			return true
		}
	}
	return false
}
