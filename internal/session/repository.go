package session

import (
	"crypto/rand"
	"encoding/hex"
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

var (
	ErrSessionNotFound  = errors.New("session not found")
	ErrPanelNotFound    = errors.New("panel not found")
	ErrRevisionNotFound = errors.New("revision not found")
	ErrRootPanel        = errors.New("root panel cannot be deleted")
)

type Repository struct {
	mu         sync.RWMutex
	root       string
	workspaces map[string]Workspace
}

func OpenRepository(root string) (*Repository, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read session directory: %w", err)
	}
	repository := &Repository{root: root, workspaces: make(map[string]Workspace)}
	for _, entry := range entries {
		if !entry.IsDir() || !validGeneratedID(entry.Name()) {
			continue
		}
		var workspace Workspace
		path := filepath.Join(root, entry.Name(), "workspace.json")
		if err := store.ReadJSON(path, &workspace); err != nil {
			return nil, fmt.Errorf("load session %q: %w", entry.Name(), err)
		}
		if err := validateWorkspace(workspace, entry.Name()); err != nil {
			return nil, fmt.Errorf("validate session %q: %w", entry.Name(), err)
		}
		workspace.Panels = canonicalPanels(workspace.Panels)
		repository.workspaces[workspace.Session.ID] = cloneWorkspace(workspace)
	}
	return repository, nil
}

func (repository *Repository) CreateSession(input CreateSessionInput) (Workspace, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Workspace{}, fmt.Errorf("session title is required")
	}
	sessionID, err := randomID()
	if err != nil {
		return Workspace{}, err
	}
	panelID, err := randomID()
	if err != nil {
		return Workspace{}, err
	}
	now := time.Now().UTC()
	workspace := Workspace{
		SchemaVersion: workspaceSchemaVersion,
		Session: Session{
			ID: sessionID, Title: title, Folder: strings.TrimSpace(input.Folder), CurrentPanelID: panelID,
			CreatedAt: now, UpdatedAt: now,
		},
		Panels: []Panel{{
			ID: panelID, Included: true, KnowledgeIDs: []string{}, AssetIDs: []string{}, Revisions: []Revision{},
			CreatedAt: now, UpdatedAt: now,
		}},
	}

	repository.mu.Lock()
	defer repository.mu.Unlock()
	if err := repository.save(workspace); err != nil {
		return Workspace{}, err
	}
	repository.workspaces[sessionID] = cloneWorkspace(workspace)
	return cloneWorkspace(workspace), nil
}

func (repository *Repository) List(filter Filter) []Session {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	query := strings.ToLower(strings.TrimSpace(filter.Query))
	result := make([]Session, 0, len(repository.workspaces))
	for _, workspace := range repository.workspaces {
		session := workspace.Session
		if filter.Folder != "" && session.Folder != filter.Folder {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(session.Folder+"\n"+session.Title), query) {
			continue
		}
		result = append(result, session)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result
}

func (repository *Repository) Get(sessionID string) (Workspace, bool) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return Workspace{}, false
	}
	return cloneWorkspace(workspace), true
}

func (repository *Repository) UpdateSession(sessionID string, input UpdateSessionInput) (Workspace, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Workspace{}, fmt.Errorf("session title is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return Workspace{}, ErrSessionNotFound
	}
	if panelIndex(workspace.Panels, input.CurrentPanelID) < 0 {
		return Workspace{}, ErrPanelNotFound
	}
	updated := cloneWorkspace(workspace)
	updated.Session.Title = title
	updated.Session.Folder = strings.TrimSpace(input.Folder)
	updated.Session.CurrentPanelID = input.CurrentPanelID
	updated.Session.UpdatedAt = nextTime(workspace.Session.UpdatedAt)
	if err := repository.save(updated); err != nil {
		return Workspace{}, err
	}
	repository.workspaces[sessionID] = updated
	return cloneWorkspace(updated), nil
}

func (repository *Repository) CreatePanel(sessionID string, input CreatePanelInput) (Panel, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return Panel{}, ErrSessionNotFound
	}
	if panelIndex(workspace.Panels, input.ParentID) < 0 {
		return Panel{}, ErrPanelNotFound
	}
	panelID, err := randomID()
	if err != nil {
		return Panel{}, err
	}
	now := nextTime(workspace.Session.UpdatedAt)
	created := Panel{
		ID: panelID, ParentID: input.ParentID, Title: input.Title, Content: input.Content,
		Included: input.Included, Collapsed: input.Collapsed, Order: nextSiblingOrder(workspace.Panels, input.ParentID),
		KnowledgeIDs: uniqueStrings(input.KnowledgeIDs), AssetIDs: uniqueStrings(input.AssetIDs), Revisions: []Revision{},
		Result: cloneResult(input.Result), CreatedAt: now, UpdatedAt: now,
	}
	updated := cloneWorkspace(workspace)
	updated.Panels = append(updated.Panels, created)
	updated.Panels = canonicalPanels(updated.Panels)
	updated.Session.CurrentPanelID = created.ID
	updated.Session.UpdatedAt = now
	if err := repository.save(updated); err != nil {
		return Panel{}, err
	}
	repository.workspaces[sessionID] = updated
	return clonePanel(created), nil
}

func (repository *Repository) UpdatePanel(sessionID, panelID string, input UpdatePanelInput) (Panel, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return Panel{}, ErrSessionNotFound
	}
	index := panelIndex(workspace.Panels, panelID)
	if index < 0 {
		return Panel{}, ErrPanelNotFound
	}
	updated := cloneWorkspace(workspace)
	panel := &updated.Panels[index]
	knowledgeIDs := uniqueStrings(input.KnowledgeIDs)
	assetIDs := uniqueStrings(input.AssetIDs)
	contentChanged := panel.Title != input.Title || panel.Content != input.Content || panel.Included != input.Included ||
		!equalStrings(panel.KnowledgeIDs, knowledgeIDs) || !equalStrings(panel.AssetIDs, assetIDs)
	now := nextTime(workspace.Session.UpdatedAt)
	if contentChanged {
		revisionID, err := randomID()
		if err != nil {
			return Panel{}, err
		}
		panel.Revisions = append(panel.Revisions, Revision{
			ID: revisionID, Title: panel.Title, Content: panel.Content, Included: panel.Included,
			KnowledgeIDs: cloneStrings(panel.KnowledgeIDs), AssetIDs: cloneStrings(panel.AssetIDs), CreatedAt: now,
		})
	}
	panel.Title = input.Title
	panel.Content = input.Content
	panel.Included = input.Included
	panel.Collapsed = input.Collapsed
	panel.KnowledgeIDs = knowledgeIDs
	panel.AssetIDs = assetIDs
	panel.UpdatedAt = now
	updated.Session.UpdatedAt = now
	if err := repository.save(updated); err != nil {
		return Panel{}, err
	}
	repository.workspaces[sessionID] = updated
	return clonePanel(*panel), nil
}

func (repository *Repository) RestoreRevision(sessionID, panelID, revisionID string) (Panel, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return Panel{}, ErrSessionNotFound
	}
	index := panelIndex(workspace.Panels, panelID)
	if index < 0 {
		return Panel{}, ErrPanelNotFound
	}
	updated := cloneWorkspace(workspace)
	panel := &updated.Panels[index]
	var target *Revision
	for revisionIndex := range panel.Revisions {
		if panel.Revisions[revisionIndex].ID == revisionID {
			copy := panel.Revisions[revisionIndex]
			target = &copy
			break
		}
	}
	if target == nil {
		return Panel{}, ErrRevisionNotFound
	}
	newRevisionID, err := randomID()
	if err != nil {
		return Panel{}, err
	}
	now := nextTime(workspace.Session.UpdatedAt)
	panel.Revisions = append(panel.Revisions, Revision{
		ID: newRevisionID, Title: panel.Title, Content: panel.Content, Included: panel.Included,
		KnowledgeIDs: cloneStrings(panel.KnowledgeIDs), AssetIDs: cloneStrings(panel.AssetIDs), CreatedAt: now,
	})
	panel.Title = target.Title
	panel.Content = target.Content
	panel.Included = target.Included
	panel.KnowledgeIDs = cloneStrings(target.KnowledgeIDs)
	panel.AssetIDs = cloneStrings(target.AssetIDs)
	panel.UpdatedAt = now
	updated.Session.UpdatedAt = now
	if err := repository.save(updated); err != nil {
		return Panel{}, err
	}
	repository.workspaces[sessionID] = updated
	return clonePanel(*panel), nil
}

func (repository *Repository) PathTo(sessionID, panelID string) ([]Panel, error) {
	repository.mu.RLock()
	defer repository.mu.RUnlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return nil, ErrSessionNotFound
	}
	return pathTo(workspace.Panels, panelID)
}

func (repository *Repository) DeletePanel(sessionID, panelID string) error {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	workspace, exists := repository.workspaces[sessionID]
	if !exists {
		return ErrSessionNotFound
	}
	index := panelIndex(workspace.Panels, panelID)
	if index < 0 {
		return ErrPanelNotFound
	}
	if workspace.Panels[index].ParentID == "" {
		return ErrRootPanel
	}
	parentID := workspace.Panels[index].ParentID
	removed := map[string]struct{}{panelID: {}}
	for changed := true; changed; {
		changed = false
		for _, panel := range workspace.Panels {
			if _, parentRemoved := removed[panel.ParentID]; !parentRemoved {
				continue
			}
			if _, alreadyRemoved := removed[panel.ID]; alreadyRemoved {
				continue
			}
			removed[panel.ID] = struct{}{}
			changed = true
		}
	}
	updated := cloneWorkspace(workspace)
	kept := make([]Panel, 0, len(updated.Panels)-len(removed))
	for _, panel := range updated.Panels {
		if _, deletePanel := removed[panel.ID]; !deletePanel {
			kept = append(kept, panel)
		}
	}
	updated.Panels = kept
	if _, currentRemoved := removed[updated.Session.CurrentPanelID]; currentRemoved {
		updated.Session.CurrentPanelID = parentID
	}
	updated.Session.UpdatedAt = nextTime(workspace.Session.UpdatedAt)
	if err := repository.save(updated); err != nil {
		return err
	}
	repository.workspaces[sessionID] = updated
	return nil
}

func (repository *Repository) ForkSession(sessionID string, input ForkSessionInput) (Workspace, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return Workspace{}, fmt.Errorf("session title is required")
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	source, exists := repository.workspaces[sessionID]
	if !exists {
		return Workspace{}, ErrSessionNotFound
	}
	path, err := pathTo(source.Panels, input.PanelID)
	if err != nil {
		return Workspace{}, err
	}
	newSessionID, err := randomID()
	if err != nil {
		return Workspace{}, err
	}
	now := time.Now().UTC()
	panels := make([]Panel, 0, len(path))
	parentID := ""
	for index, sourcePanel := range path {
		newPanelID, err := randomID()
		if err != nil {
			return Workspace{}, err
		}
		createdAt := now.Add(time.Duration(index) * time.Nanosecond)
		panels = append(panels, Panel{
			ID: newPanelID, ParentID: parentID, Title: sourcePanel.Title, Content: sourcePanel.Content,
			Included: sourcePanel.Included, Collapsed: sourcePanel.Collapsed, Order: sourcePanel.Order,
			KnowledgeIDs: cloneStrings(sourcePanel.KnowledgeIDs), AssetIDs: cloneStrings(sourcePanel.AssetIDs), Revisions: []Revision{},
			CreatedAt: createdAt, UpdatedAt: createdAt,
		})
		parentID = newPanelID
	}
	forked := Workspace{
		SchemaVersion: workspaceSchemaVersion,
		Session: Session{
			ID: newSessionID, Title: title, Folder: strings.TrimSpace(input.Folder), CurrentPanelID: panels[len(panels)-1].ID,
			CreatedAt: now, UpdatedAt: panels[len(panels)-1].UpdatedAt,
		},
		Panels: panels,
	}
	if err := repository.save(forked); err != nil {
		return Workspace{}, err
	}
	repository.workspaces[newSessionID] = cloneWorkspace(forked)
	return cloneWorkspace(forked), nil
}

func (repository *Repository) restoreWorkspace(workspace Workspace) error {
	if err := validateWorkspace(workspace, workspace.Session.ID); err != nil {
		return err
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	restored := cloneWorkspace(workspace)
	restored.Panels = canonicalPanels(restored.Panels)
	if err := repository.save(restored); err != nil {
		return err
	}
	repository.workspaces[restored.Session.ID] = restored
	return nil
}

func (repository *Repository) deleteWorkspace(sessionID string) error {
	if !validGeneratedID(sessionID) {
		return ErrSessionNotFound
	}
	repository.mu.Lock()
	defer repository.mu.Unlock()
	if _, exists := repository.workspaces[sessionID]; !exists {
		return ErrSessionNotFound
	}
	path := filepath.Join(repository.root, sessionID)
	if err := os.RemoveAll(path); err != nil {
		return fmt.Errorf("delete session workspace: %w", err)
	}
	delete(repository.workspaces, sessionID)
	return nil
}

func (repository *Repository) save(workspace Workspace) error {
	path := filepath.Join(repository.root, workspace.Session.ID, "workspace.json")
	return store.WriteJSON(path, workspace, 0o600)
}

func validateWorkspace(workspace Workspace, directoryID string) error {
	if workspace.SchemaVersion != workspaceSchemaVersion {
		return fmt.Errorf("workspace schema version %d is unsupported", workspace.SchemaVersion)
	}
	if workspace.Session.ID != directoryID || !validGeneratedID(workspace.Session.ID) {
		return fmt.Errorf("session ID does not match its directory")
	}
	if strings.TrimSpace(workspace.Session.Title) == "" {
		return fmt.Errorf("session title is required")
	}
	if len(workspace.Panels) == 0 {
		return fmt.Errorf("workspace must contain a root panel")
	}
	byID := make(map[string]Panel, len(workspace.Panels))
	rootCount := 0
	for _, panel := range workspace.Panels {
		if !validGeneratedID(panel.ID) {
			return fmt.Errorf("invalid panel ID %q", panel.ID)
		}
		if _, duplicate := byID[panel.ID]; duplicate {
			return fmt.Errorf("duplicate panel ID %q", panel.ID)
		}
		byID[panel.ID] = panel
		if panel.ParentID == "" {
			rootCount++
		}
	}
	if rootCount != 1 {
		return fmt.Errorf("workspace must contain exactly one root panel")
	}
	if _, exists := byID[workspace.Session.CurrentPanelID]; !exists {
		return fmt.Errorf("current panel does not exist")
	}
	for _, panel := range workspace.Panels {
		if panel.ParentID != "" {
			if _, exists := byID[panel.ParentID]; !exists {
				return fmt.Errorf("panel %q has a missing parent", panel.ID)
			}
		}
		seen := map[string]struct{}{panel.ID: {}}
		cursor := panel
		for cursor.ParentID != "" {
			if _, duplicate := seen[cursor.ParentID]; duplicate {
				return fmt.Errorf("panel tree contains a cycle")
			}
			seen[cursor.ParentID] = struct{}{}
			cursor = byID[cursor.ParentID]
		}
	}
	return nil
}

func pathTo(panels []Panel, panelID string) ([]Panel, error) {
	byID := make(map[string]Panel, len(panels))
	for _, panel := range panels {
		byID[panel.ID] = panel
	}
	cursor, exists := byID[panelID]
	if !exists {
		return nil, ErrPanelNotFound
	}
	reversed := make([]Panel, 0)
	for {
		reversed = append(reversed, clonePanel(cursor))
		if cursor.ParentID == "" {
			break
		}
		cursor = byID[cursor.ParentID]
	}
	path := make([]Panel, len(reversed))
	for index := range reversed {
		path[len(reversed)-1-index] = reversed[index]
	}
	return path, nil
}

func canonicalPanels(panels []Panel) []Panel {
	children := make(map[string][]Panel)
	var root Panel
	for _, panel := range panels {
		cloned := clonePanel(panel)
		if cloned.ParentID == "" {
			root = cloned
			continue
		}
		children[cloned.ParentID] = append(children[cloned.ParentID], cloned)
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(left, right int) bool {
			first, second := children[parentID][left], children[parentID][right]
			if first.Order != second.Order {
				return first.Order < second.Order
			}
			if !first.CreatedAt.Equal(second.CreatedAt) {
				return first.CreatedAt.Before(second.CreatedAt)
			}
			return first.ID < second.ID
		})
	}
	result := make([]Panel, 0, len(panels))
	var appendSubtree func(Panel)
	appendSubtree = func(panel Panel) {
		result = append(result, panel)
		for _, child := range children[panel.ID] {
			appendSubtree(child)
		}
	}
	appendSubtree(root)
	return result
}

func cloneWorkspace(workspace Workspace) Workspace {
	clone := workspace
	clone.Panels = make([]Panel, len(workspace.Panels))
	for index, panel := range workspace.Panels {
		clone.Panels[index] = clonePanel(panel)
	}
	return clone
}

func clonePanel(panel Panel) Panel {
	clone := panel
	clone.KnowledgeIDs = cloneStrings(panel.KnowledgeIDs)
	clone.AssetIDs = cloneStrings(panel.AssetIDs)
	clone.Revisions = make([]Revision, len(panel.Revisions))
	for index, revision := range panel.Revisions {
		clone.Revisions[index] = revision
		clone.Revisions[index].KnowledgeIDs = cloneStrings(revision.KnowledgeIDs)
		clone.Revisions[index].AssetIDs = cloneStrings(revision.AssetIDs)
	}
	clone.Result = cloneResult(panel.Result)
	return clone
}

func cloneResult(result *ResultMetadata) *ResultMetadata {
	if result == nil {
		return nil
	}
	clone := *result
	return &clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return append([]string{}, values...)
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
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

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func panelIndex(panels []Panel, panelID string) int {
	for index := range panels {
		if panels[index].ID == panelID {
			return index
		}
	}
	return -1
}

func nextSiblingOrder(panels []Panel, parentID string) int {
	next := 0
	for _, panel := range panels {
		if panel.ParentID == parentID && panel.Order >= next {
			next = panel.Order + 1
		}
	}
	return next
}

func nextTime(previous time.Time) time.Time {
	now := time.Now().UTC()
	if !now.After(previous) {
		return previous.Add(time.Nanosecond)
	}
	return now
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate session ID: %w", err)
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
