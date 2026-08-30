package session

import (
	"errors"
	"reflect"
	"testing"
)

func TestRepositoryBuildsCurrentPathAndStableSiblingBranches(t *testing.T) {
	repository, err := OpenRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := repository.CreateSession(CreateSessionInput{Title: "Research", Folder: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Panels) != 1 || !workspace.Panels[0].Included || workspace.Session.CurrentPanelID != workspace.Panels[0].ID {
		t.Fatalf("new workspace = %#v", workspace)
	}
	root := workspace.Panels[0]
	left, err := repository.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID: root.ID, Title: "Left", Content: "a", Included: true})
	if err != nil {
		t.Fatal(err)
	}
	right, err := repository.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID: root.ID, Title: "Right", Content: "b", Included: true})
	if err != nil {
		t.Fatal(err)
	}

	path, err := repository.PathTo(workspace.Session.ID, right.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].ID != root.ID || path[1].ID != right.ID {
		t.Fatalf("path = %#v", path)
	}
	got, exists := repository.Get(workspace.Session.ID)
	if !exists {
		t.Fatal("workspace disappeared")
	}
	if got.Session.CurrentPanelID != right.ID {
		t.Fatalf("current panel = %q, want %q", got.Session.CurrentPanelID, right.ID)
	}
	siblings := childPanels(got.Panels, root.ID)
	if len(siblings) != 2 || siblings[0].ID != left.ID || siblings[1].ID != right.ID || siblings[0].Order >= siblings[1].Order {
		t.Fatalf("siblings = %#v", siblings)
	}
}

func TestRepositoryRejectsMissingAndCrossSessionParents(t *testing.T) {
	repository, err := OpenRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.CreateSession(CreateSessionInput{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateSession(CreateSessionInput{Title: "second"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := repository.CreatePanel(first.Session.ID, CreatePanelInput{ParentID: "missing", Title: "bad"}); !errors.Is(err, ErrPanelNotFound) {
		t.Fatalf("missing parent error = %v", err)
	}
	if _, err := repository.CreatePanel(first.Session.ID, CreatePanelInput{ParentID: second.Panels[0].ID, Title: "cross"}); !errors.Is(err, ErrPanelNotFound) {
		t.Fatalf("cross-session parent error = %v", err)
	}
	if err := repository.DeletePanel(first.Session.ID, first.Panels[0].ID); !errors.Is(err, ErrRootPanel) {
		t.Fatalf("root deletion error = %v", err)
	}
	if _, err := repository.PathTo(first.Session.ID, second.Panels[0].ID); !errors.Is(err, ErrPanelNotFound) {
		t.Fatalf("cross-session path error = %v", err)
	}
}

func TestUpdateCreatesRestorableRevisionButCollapseDoesNot(t *testing.T) {
	repository, err := OpenRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := repository.CreateSession(CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	updated, err := repository.UpdatePanel(workspace.Session.ID, root.ID, UpdatePanelInput{
		Title: "new", Content: "new", Included: true, KnowledgeIDs: []string{"knowledge-1"}, AssetIDs: []string{"asset-1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Revisions) != 1 || updated.Revisions[0].Content != "" || !updated.Revisions[0].Included {
		t.Fatalf("updated panel = %#v", updated)
	}
	collapsed, err := repository.UpdatePanel(workspace.Session.ID, root.ID, UpdatePanelInput{
		Title: updated.Title, Content: updated.Content, Included: updated.Included, Collapsed: true,
		KnowledgeIDs: updated.KnowledgeIDs, AssetIDs: updated.AssetIDs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(collapsed.Revisions) != 1 || !collapsed.Collapsed {
		t.Fatalf("collapse created a content revision: %#v", collapsed)
	}
	restored, err := repository.RestoreRevision(workspace.Session.ID, root.ID, updated.Revisions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Content != "" || restored.Title != "" || len(restored.Revisions) != 2 || restored.Collapsed != collapsed.Collapsed {
		t.Fatalf("restored panel = %#v", restored)
	}
	if _, err := repository.RestoreRevision(workspace.Session.ID, root.ID, "missing"); !errors.Is(err, ErrRevisionNotFound) {
		t.Fatalf("missing revision error = %v", err)
	}
}

func TestDeletePanelRemovesWholeSubtreeAndMovesCurrentToParent(t *testing.T) {
	repository, err := OpenRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := repository.CreateSession(CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	child, err := repository.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID: root.ID, Title: "child"})
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := repository.CreatePanel(workspace.Session.ID, CreatePanelInput{ParentID: child.ID, Title: "grandchild"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.DeletePanel(workspace.Session.ID, child.ID); err != nil {
		t.Fatal(err)
	}
	got, exists := repository.Get(workspace.Session.ID)
	if !exists {
		t.Fatal("workspace disappeared")
	}
	if panelExists(got.Panels, child.ID) || panelExists(got.Panels, grandchild.ID) {
		t.Fatalf("subtree remains: %#v", got.Panels)
	}
	if got.Session.CurrentPanelID != root.ID {
		t.Fatalf("current panel = %q, want root %q", got.Session.CurrentPanelID, root.ID)
	}
	if err := repository.DeletePanel(workspace.Session.ID, root.ID); !errors.Is(err, ErrRootPanel) {
		t.Fatalf("root deletion error = %v", err)
	}
}

func TestForkSessionCopiesOnlyChosenPathWithFreshIDsAndNoResults(t *testing.T) {
	repository, err := OpenRepository(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, err := repository.CreateSession(CreateSessionInput{Title: "source"})
	if err != nil {
		t.Fatal(err)
	}
	root := source.Panels[0]
	chosen, err := repository.CreatePanel(source.Session.ID, CreatePanelInput{
		ParentID: root.ID, Title: "chosen", Content: "keep", Included: true,
		KnowledgeIDs: []string{"knowledge"}, AssetIDs: []string{"asset"}, Result: &ResultMetadata{RunID: "run"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreatePanel(source.Session.ID, CreatePanelInput{ParentID: root.ID, Title: "sibling"}); err != nil {
		t.Fatal(err)
	}
	forked, err := repository.ForkSession(source.Session.ID, ForkSessionInput{PanelID: chosen.ID, Title: "fork", Folder: "copies"})
	if err != nil {
		t.Fatal(err)
	}
	if forked.Session.Title != "fork" || forked.Session.Folder != "copies" || len(forked.Panels) != 2 {
		t.Fatalf("forked workspace = %#v", forked)
	}
	if forked.Panels[0].ID == root.ID || forked.Panels[1].ID == chosen.ID || forked.Panels[1].ParentID != forked.Panels[0].ID {
		t.Fatalf("forked IDs = %#v", forked.Panels)
	}
	if forked.Panels[1].Content != "keep" || !reflect.DeepEqual(forked.Panels[1].KnowledgeIDs, []string{"knowledge"}) || !reflect.DeepEqual(forked.Panels[1].AssetIDs, []string{"asset"}) {
		t.Fatalf("forked content = %#v", forked.Panels[1])
	}
	if forked.Panels[1].Result != nil || len(forked.Panels[1].Revisions) != 0 {
		t.Fatalf("fork copied execution history: %#v", forked.Panels[1])
	}
}

func TestRepositoryPersistsUpdatesAndReturnsDeepCopies(t *testing.T) {
	rootDirectory := t.TempDir()
	repository, err := OpenRepository(rootDirectory)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateSession(CreateSessionInput{Title: "before", Folder: "old"})
	if err != nil {
		t.Fatal(err)
	}
	child, err := repository.CreatePanel(created.Session.ID, CreatePanelInput{
		ParentID: created.Panels[0].ID, Title: "child", Included: true, AssetIDs: []string{"asset"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := repository.UpdateSession(created.Session.ID, UpdateSessionInput{Title: "after", Folder: "new", CurrentPanelID: child.ID})
	if err != nil {
		t.Fatal(err)
	}
	updated.Panels[1].AssetIDs[0] = "mutated"

	reopened, err := OpenRepository(rootDirectory)
	if err != nil {
		t.Fatal(err)
	}
	got, exists := reopened.Get(created.Session.ID)
	if !exists {
		t.Fatal("reopened workspace missing")
	}
	if got.Session.Title != "after" || got.Session.Folder != "new" || got.Session.CurrentPanelID != child.ID || got.Panels[1].AssetIDs[0] != "asset" {
		t.Fatalf("reopened workspace = %#v", got)
	}
	if sessions := reopened.List(Filter{Folder: "new", Query: "AFTER"}); len(sessions) != 1 || sessions[0].ID != created.Session.ID {
		t.Fatalf("filtered sessions = %#v", sessions)
	}
}

func panelExists(panels []Panel, panelID string) bool {
	for _, panel := range panels {
		if panel.ID == panelID {
			return true
		}
	}
	return false
}

func childPanels(panels []Panel, parentID string) []Panel {
	children := make([]Panel, 0)
	for _, panel := range panels {
		if panel.ParentID == parentID {
			children = append(children, panel)
		}
	}
	return children
}
