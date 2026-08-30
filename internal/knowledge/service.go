package knowledge

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ekk1/ai-desktop/internal/asset"
)

var ErrAssetNotFound = errors.New("referenced asset not found")

type Service struct {
	mu     sync.Mutex
	notes  *Repository
	assets *asset.Repository
}

func NewService(notes *Repository, assets *asset.Repository) *Service {
	return &Service{notes: notes, assets: assets}
}

func (service *Service) List(filter Filter) []Note {
	return service.notes.List(filter)
}

func (service *Service) Get(id string) (Note, bool) {
	return service.notes.Get(id)
}

func (service *Service) Create(input Input) (Note, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	normalized, err := normalizeInput(input)
	if err != nil {
		return Note{}, err
	}
	if err := service.validateAssets(normalized.AssetIDs); err != nil {
		return Note{}, err
	}
	created, err := service.notes.Create(normalized)
	if err != nil {
		return Note{}, err
	}
	added := make([]string, 0, len(created.AssetIDs))
	for _, assetID := range created.AssetIDs {
		if _, err := service.assets.AddReference(assetID, noteReference(created.ID)); err != nil {
			rollbackErrors := service.removeReferences(added, created.ID)
			rollbackErrors = append(rollbackErrors, service.notes.Delete(created.ID))
			return Note{}, joinedError(err, rollbackErrors...)
		}
		added = append(added, assetID)
	}
	return created, nil
}

func (service *Service) Update(id string, input Input) (Note, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	previous, ok := service.notes.Get(id)
	if !ok {
		return Note{}, ErrNotFound
	}
	normalized, err := normalizeInput(input)
	if err != nil {
		return Note{}, err
	}
	if err := service.validateAssets(normalized.AssetIDs); err != nil {
		return Note{}, err
	}
	added, removed := difference(previous.AssetIDs, normalized.AssetIDs)
	completedAdds := make([]string, 0, len(added))
	for _, assetID := range added {
		if _, err := service.assets.AddReference(assetID, noteReference(id)); err != nil {
			return Note{}, joinedError(err, service.removeReferences(completedAdds, id)...)
		}
		completedAdds = append(completedAdds, assetID)
	}
	updated, err := service.notes.Update(id, normalized)
	if err != nil {
		return Note{}, joinedError(err, service.removeReferences(completedAdds, id)...)
	}
	completedRemovals := make([]string, 0, len(removed))
	for _, assetID := range removed {
		if _, err := service.assets.RemoveReference(assetID, noteReference(id)); err != nil {
			rollbackErrors := service.addReferences(completedRemovals, id)
			rollbackErrors = append(rollbackErrors, service.notes.restore(previous))
			rollbackErrors = append(rollbackErrors, service.removeReferences(completedAdds, id)...)
			return Note{}, joinedError(err, rollbackErrors...)
		}
		completedRemovals = append(completedRemovals, assetID)
	}
	return updated, nil
}

func (service *Service) Delete(id string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	previous, ok := service.notes.Get(id)
	if !ok {
		return ErrNotFound
	}
	removed := make([]string, 0, len(previous.AssetIDs))
	for _, assetID := range previous.AssetIDs {
		if _, err := service.assets.RemoveReference(assetID, noteReference(id)); err != nil {
			return joinedError(err, service.addReferences(removed, id)...)
		}
		removed = append(removed, assetID)
	}
	if err := service.notes.Delete(id); err != nil {
		return joinedError(err, service.addReferences(removed, id)...)
	}
	return nil
}

func (service *Service) validateAssets(ids []string) error {
	for _, id := range ids {
		if _, ok := service.assets.Get(id); !ok {
			return fmt.Errorf("%w: %s", ErrAssetNotFound, id)
		}
	}
	return nil
}

func (service *Service) removeReferences(ids []string, noteID string) []error {
	errorsFound := make([]error, 0)
	for _, id := range ids {
		if _, err := service.assets.RemoveReference(id, noteReference(noteID)); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errorsFound
}

func (service *Service) addReferences(ids []string, noteID string) []error {
	errorsFound := make([]error, 0)
	for _, id := range ids {
		if _, err := service.assets.AddReference(id, noteReference(noteID)); err != nil {
			errorsFound = append(errorsFound, err)
		}
	}
	return errorsFound
}

func noteReference(noteID string) asset.Reference {
	return asset.Reference{Module: "knowledge", RecordID: noteID}
}

func difference(previous, next []string) (added, removed []string) {
	previousSet := make(map[string]struct{}, len(previous))
	nextSet := make(map[string]struct{}, len(next))
	for _, id := range previous {
		previousSet[id] = struct{}{}
	}
	for _, id := range next {
		nextSet[id] = struct{}{}
		if _, exists := previousSet[id]; !exists {
			added = append(added, id)
		}
	}
	for _, id := range previous {
		if _, exists := nextSet[id]; !exists {
			removed = append(removed, id)
		}
	}
	return added, removed
}

func joinedError(primary error, rollback ...error) error {
	all := []error{primary}
	for _, err := range rollback {
		if err != nil {
			all = append(all, fmt.Errorf("rollback: %w", err))
		}
	}
	return errors.Join(all...)
}
