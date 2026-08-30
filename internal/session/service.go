package session

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ekk1/ai-desktop/internal/asset"
)

const panelReferenceModule = "session_panel"

var ErrAssetNotFound = errors.New("panel asset not found")

type Service struct {
	mu         sync.RWMutex
	repository *Repository
	assets     *asset.Repository
}

type assetReferenceChange struct {
	assetID   string
	reference asset.Reference
}

func NewService(repository *Repository, assets *asset.Repository) *Service {
	return &Service{repository: repository, assets: assets}
}

func (service *Service) List(filter Filter) []Session {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.repository.List(filter)
}

func (service *Service) Get(sessionID string) (Workspace, bool) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.repository.Get(sessionID)
}

func (service *Service) PathTo(sessionID, panelID string) ([]Panel, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.repository.PathTo(sessionID, panelID)
}

func (service *Service) CreateSession(input CreateSessionInput) (Workspace, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.repository.CreateSession(input)
}

func (service *Service) UpdateSession(sessionID string, input UpdateSessionInput) (Workspace, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.repository.UpdateSession(sessionID, input)
}

func (service *Service) DeleteSession(sessionID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	before, exists := service.repository.Get(sessionID)
	if !exists {
		return ErrSessionNotFound
	}
	changes := make([]assetReferenceChange, 0)
	for _, panel := range before.Panels {
		changes = append(changes, changesForPanel(panel)...)
	}
	if err := service.repository.deleteWorkspace(sessionID); err != nil {
		return err
	}
	completed, err := service.removeReferencesUntilError(changes)
	if err == nil {
		return nil
	}
	rollbackErrors := []error{service.repository.restoreWorkspace(before)}
	rollbackErrors = append(rollbackErrors, service.addReferencesCollectErrors(completed)...)
	return joinMutationError(err, rollbackErrors)
}

func (service *Service) CreatePanel(sessionID string, input CreatePanelInput) (Panel, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.validateAssets(input.AssetIDs); err != nil {
		return Panel{}, err
	}
	before, exists := service.repository.Get(sessionID)
	if !exists {
		return Panel{}, ErrSessionNotFound
	}
	created, err := service.repository.CreatePanel(sessionID, input)
	if err != nil {
		return Panel{}, err
	}
	changes := changesForPanel(created)
	completed, err := service.addReferences(changes)
	if err == nil {
		return created, nil
	}
	rollbackErrors := []error{service.repository.restoreWorkspace(before)}
	rollbackErrors = append(rollbackErrors, service.removeReferences(completed)...)
	return Panel{}, joinMutationError(err, rollbackErrors)
}

func (service *Service) UpdatePanel(sessionID, panelID string, input UpdatePanelInput) (Panel, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.mutatePanelAssets(sessionID, panelID, input.AssetIDs, func() (Panel, error) {
		return service.repository.UpdatePanel(sessionID, panelID, input)
	})
}

func (service *Service) RestoreRevision(sessionID, panelID, revisionID string) (Panel, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	workspace, panel, err := service.workspacePanel(sessionID, panelID)
	if err != nil {
		return Panel{}, err
	}
	var target *Revision
	for index := range panel.Revisions {
		if panel.Revisions[index].ID == revisionID {
			copy := panel.Revisions[index]
			target = &copy
			break
		}
	}
	if target == nil {
		return Panel{}, ErrRevisionNotFound
	}
	return service.mutatePanelAssetsFrom(workspace, panel, target.AssetIDs, func() (Panel, error) {
		return service.repository.RestoreRevision(sessionID, panelID, revisionID)
	})
}

func (service *Service) DeletePanel(sessionID, panelID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	before, exists := service.repository.Get(sessionID)
	if !exists {
		return ErrSessionNotFound
	}
	index := panelIndex(before.Panels, panelID)
	if index < 0 {
		return ErrPanelNotFound
	}
	if before.Panels[index].ParentID == "" {
		return ErrRootPanel
	}
	removedIDs := map[string]struct{}{panelID: {}}
	for changed := true; changed; {
		changed = false
		for _, panel := range before.Panels {
			if _, parentRemoved := removedIDs[panel.ParentID]; !parentRemoved {
				continue
			}
			if _, present := removedIDs[panel.ID]; present {
				continue
			}
			removedIDs[panel.ID] = struct{}{}
			changed = true
		}
	}
	changes := make([]assetReferenceChange, 0)
	for _, panel := range before.Panels {
		if _, removed := removedIDs[panel.ID]; removed {
			changes = append(changes, changesForPanel(panel)...)
		}
	}
	if err := service.repository.DeletePanel(sessionID, panelID); err != nil {
		return err
	}
	completed, err := service.removeReferencesUntilError(changes)
	if err == nil {
		return nil
	}
	rollbackErrors := []error{service.repository.restoreWorkspace(before)}
	rollbackErrors = append(rollbackErrors, service.addReferencesCollectErrors(completed)...)
	return joinMutationError(err, rollbackErrors)
}

func (service *Service) ForkSession(sessionID string, input ForkSessionInput) (Workspace, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	source, exists := service.repository.Get(sessionID)
	if !exists {
		return Workspace{}, ErrSessionNotFound
	}
	path, err := pathTo(source.Panels, input.PanelID)
	if err != nil {
		return Workspace{}, err
	}
	for _, panel := range path {
		if err := service.validateAssets(panel.AssetIDs); err != nil {
			return Workspace{}, err
		}
	}
	forked, err := service.repository.ForkSession(sessionID, input)
	if err != nil {
		return Workspace{}, err
	}
	changes := make([]assetReferenceChange, 0)
	for _, panel := range forked.Panels {
		changes = append(changes, changesForPanel(panel)...)
	}
	completed, err := service.addReferences(changes)
	if err == nil {
		return forked, nil
	}
	rollbackErrors := []error{service.repository.deleteWorkspace(forked.Session.ID)}
	rollbackErrors = append(rollbackErrors, service.removeReferences(completed)...)
	return Workspace{}, joinMutationError(err, rollbackErrors)
}

func (service *Service) mutatePanelAssets(
	sessionID, panelID string,
	assetIDs []string,
	mutation func() (Panel, error),
) (Panel, error) {
	workspace, panel, err := service.workspacePanel(sessionID, panelID)
	if err != nil {
		return Panel{}, err
	}
	return service.mutatePanelAssetsFrom(workspace, panel, assetIDs, mutation)
}

func (service *Service) mutatePanelAssetsFrom(
	before Workspace,
	oldPanel Panel,
	assetIDs []string,
	mutation func() (Panel, error),
) (Panel, error) {
	if err := service.validateAssets(assetIDs); err != nil {
		return Panel{}, err
	}
	newIDs := stringSet(assetIDs)
	oldIDs := stringSet(oldPanel.AssetIDs)
	adds := referenceDifference(newIDs, oldIDs, oldPanel.ID)
	removes := referenceDifference(oldIDs, newIDs, oldPanel.ID)
	completedAdds, err := service.addReferences(adds)
	if err != nil {
		return Panel{}, joinMutationError(err, service.removeReferences(completedAdds))
	}
	updated, err := mutation()
	if err != nil {
		return Panel{}, joinMutationError(err, service.removeReferences(completedAdds))
	}
	completedRemoves, err := service.removeReferencesUntilError(removes)
	if err == nil {
		return updated, nil
	}
	rollbackErrors := []error{service.repository.restoreWorkspace(before)}
	rollbackErrors = append(rollbackErrors, service.addReferencesCollectErrors(completedRemoves)...)
	rollbackErrors = append(rollbackErrors, service.removeReferences(completedAdds)...)
	return Panel{}, joinMutationError(err, rollbackErrors)
}

func (service *Service) workspacePanel(sessionID, panelID string) (Workspace, Panel, error) {
	workspace, exists := service.repository.Get(sessionID)
	if !exists {
		return Workspace{}, Panel{}, ErrSessionNotFound
	}
	index := panelIndex(workspace.Panels, panelID)
	if index < 0 {
		return Workspace{}, Panel{}, ErrPanelNotFound
	}
	return workspace, workspace.Panels[index], nil
}

func (service *Service) validateAssets(assetIDs []string) error {
	for assetID := range stringSet(assetIDs) {
		if _, exists := service.assets.Get(assetID); !exists {
			return fmt.Errorf("%w: %s", ErrAssetNotFound, assetID)
		}
	}
	return nil
}

func (service *Service) addReferences(changes []assetReferenceChange) ([]assetReferenceChange, error) {
	completed := make([]assetReferenceChange, 0, len(changes))
	for _, change := range changes {
		if _, err := service.assets.AddReference(change.assetID, change.reference); err != nil {
			return completed, fmt.Errorf("add panel asset reference: %w", err)
		}
		completed = append(completed, change)
	}
	return completed, nil
}

func (service *Service) removeReferencesUntilError(changes []assetReferenceChange) ([]assetReferenceChange, error) {
	completed := make([]assetReferenceChange, 0, len(changes))
	for _, change := range changes {
		if _, err := service.assets.RemoveReference(change.assetID, change.reference); err != nil {
			return completed, fmt.Errorf("remove panel asset reference: %w", err)
		}
		completed = append(completed, change)
	}
	return completed, nil
}

func (service *Service) removeReferences(changes []assetReferenceChange) []error {
	errorsFound := make([]error, 0)
	for _, change := range changes {
		if _, err := service.assets.RemoveReference(change.assetID, change.reference); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("remove panel asset reference during rollback: %w", err))
		}
	}
	return errorsFound
}

func (service *Service) addReferencesCollectErrors(changes []assetReferenceChange) []error {
	errorsFound := make([]error, 0)
	for _, change := range changes {
		if _, err := service.assets.AddReference(change.assetID, change.reference); err != nil {
			errorsFound = append(errorsFound, fmt.Errorf("restore panel asset reference during rollback: %w", err))
		}
	}
	return errorsFound
}

func changesForPanel(panel Panel) []assetReferenceChange {
	changes := make([]assetReferenceChange, 0, len(panel.AssetIDs))
	for assetID := range stringSet(panel.AssetIDs) {
		changes = append(changes, assetReferenceChange{
			assetID: assetID, reference: asset.Reference{Module: panelReferenceModule, RecordID: panel.ID},
		})
	}
	return changes
}

func referenceDifference(included, excluded map[string]struct{}, panelID string) []assetReferenceChange {
	changes := make([]assetReferenceChange, 0)
	for assetID := range included {
		if _, exists := excluded[assetID]; exists {
			continue
		}
		changes = append(changes, assetReferenceChange{
			assetID: assetID, reference: asset.Reference{Module: panelReferenceModule, RecordID: panelID},
		})
	}
	return changes
}

func stringSet(values []string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value != "" {
			set[value] = struct{}{}
		}
	}
	return set
}

func joinMutationError(primary error, rollbackErrors []error) error {
	combined := []error{primary}
	for _, rollbackErr := range rollbackErrors {
		if rollbackErr != nil {
			combined = append(combined, rollbackErr)
		}
	}
	return errors.Join(combined...)
}
