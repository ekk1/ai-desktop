package llm

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
)

func TestAssemblerUsesCurrentIncludedPathAndKnowledgeOrder(t *testing.T) {
	fixture := newAssemblerFixture(t)
	note, err := fixture.knowledge.Create(knowledge.Input{Title: "Knowledge title", Content: "Knowledge body"})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := fixture.sessions.CreateSession(session.CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	if _, err := fixture.sessions.UpdatePanel(workspace.Session.ID, root.ID, session.UpdatePanelInput{
		Content: "root", Included: true, KnowledgeIDs: []string{note.ID},
	}); err != nil {
		t.Fatal(err)
	}
	skipped, err := fixture.sessions.CreatePanel(workspace.Session.ID, session.CreatePanelInput{
		ParentID: root.ID, Content: "skip me", Included: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := fixture.sessions.CreatePanel(workspace.Session.ID, session.CreatePanelInput{
		ParentID: skipped.ID, Content: "leaf", Included: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace, _ = fixture.sessions.Get(workspace.Session.ID)
	configuration := provider.DefaultLLMConfig()
	prepared, snapshot, err := fixture.assembler.Build(workspace, leaf.ID, configuration.Providers[0], configuration.QuickPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "root\n\nKnowledge title\nKnowledge body\n\nleaf" {
		t.Fatalf("content = %q", snapshot.Content)
	}
	if len(snapshot.Panels) != 2 || snapshot.Panels[0].ID != root.ID || snapshot.Panels[1].ID != leaf.ID || len(snapshot.Knowledge) != 1 {
		t.Fatalf("snapshot context = %#v", snapshot)
	}
	if !bytes.Contains(prepared.Body, []byte(`"prompt":"root\n\nKnowledge title\nKnowledge body\n\nleaf"`)) {
		t.Fatalf("prepared body = %s", prepared.Body)
	}
}

func TestAssemblerIncludesPanelAndKnowledgeImageAssetsAsDataURLs(t *testing.T) {
	fixture := newAssemblerFixture(t)
	first := fixture.importAsset(t, "first.png", "image/png", []byte("first image"))
	second := fixture.importAsset(t, "second.png", "image/png", []byte("second image"))
	note, err := fixture.knowledge.Create(knowledge.Input{Title: "K", AssetIDs: []string{first.ID}})
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := fixture.sessions.CreateSession(session.CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	if _, err := fixture.sessions.UpdatePanel(workspace.Session.ID, root.ID, session.UpdatePanelInput{
		Included: true, KnowledgeIDs: []string{note.ID}, AssetIDs: []string{second.ID},
	}); err != nil {
		t.Fatal(err)
	}
	workspace, _ = fixture.sessions.Get(workspace.Session.ID)
	configuration := provider.DefaultLLMConfig()
	_, snapshot, err := fixture.assembler.Build(workspace, root.ID, configuration.Providers[0], configuration.QuickPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.AssetDataURLs) != 2 || !strings.HasPrefix(snapshot.AssetDataURLs[0], "data:image/png;base64,") || !strings.HasPrefix(snapshot.AssetDataURLs[1], "data:image/png;base64,") {
		t.Fatalf("asset data URLs = %#v", snapshot.AssetDataURLs)
	}
	if len(snapshot.Assets) != 2 || snapshot.Assets[0].ID != second.ID || snapshot.Assets[1].ID != first.ID {
		t.Fatalf("asset order = %#v", snapshot.Assets)
	}
}

func TestAssemblerRejectsNonImageAndOversizedAssetsBeforeSnapshot(t *testing.T) {
	fixture := newAssemblerFixture(t)
	textAsset := fixture.importAsset(t, "note.txt", "text/plain", []byte("text"))
	workspace, err := fixture.sessions.CreateSession(session.CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	root := workspace.Panels[0]
	if _, err := fixture.sessions.UpdatePanel(workspace.Session.ID, root.ID, session.UpdatePanelInput{Included: true, AssetIDs: []string{textAsset.ID}}); err != nil {
		t.Fatal(err)
	}
	workspace, _ = fixture.sessions.Get(workspace.Session.ID)
	configuration := provider.DefaultLLMConfig()
	if _, _, err := fixture.assembler.Build(workspace, root.ID, configuration.Providers[0], configuration.QuickPaths[0]); !errors.Is(err, ErrUnsupportedAsset) {
		t.Fatalf("non-image error = %v", err)
	}

	imageAsset := fixture.importAsset(t, "large.png", "image/png", []byte("larger than one byte"))
	if _, err := fixture.sessions.UpdatePanel(workspace.Session.ID, root.ID, session.UpdatePanelInput{Included: true, AssetIDs: []string{imageAsset.ID}}); err != nil {
		t.Fatal(err)
	}
	workspace, _ = fixture.sessions.Get(workspace.Session.ID)
	providerConfig := configuration.Providers[0]
	providerConfig.MaxAssetBytes = 1
	if _, _, err := fixture.assembler.Build(workspace, root.ID, providerConfig, configuration.QuickPaths[0]); !errors.Is(err, ErrAssetLimit) {
		t.Fatalf("asset limit error = %v", err)
	}
}

func TestAssemblerSnapshotRedactsProviderSecrets(t *testing.T) {
	fixture := newAssemblerFixture(t)
	workspace, err := fixture.sessions.CreateSession(session.CreateSessionInput{Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	configuration := provider.DefaultLLMConfig()
	providerConfig := configuration.Providers[0]
	providerConfig.APIKey = "top-secret"
	providerConfig.Headers["Authorization"] = "Bearer ${API_KEY}"
	_, snapshot, err := fixture.assembler.Build(workspace, workspace.Panels[0].ID, providerConfig, configuration.QuickPaths[0])
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Provider.APIKey != "" || snapshot.Headers["Authorization"] != "<redacted>" || strings.Contains(string(snapshot.Body), "top-secret") {
		t.Fatalf("snapshot leaked secret: %#v", snapshot)
	}
}

type assemblerFixture struct {
	assembler *Assembler
	sessions  *session.Service
	knowledge *knowledge.Service
	assets    *asset.Repository
}

func newAssemblerFixture(t *testing.T) assemblerFixture {
	t.Helper()
	root := t.TempDir()
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	notes, err := knowledge.OpenRepository(filepath.Join(root, "knowledge", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	knowledgeService := knowledge.NewService(notes, assets)
	sessions, err := session.OpenRepository(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sessionService := session.NewService(sessions, assets)
	return assemblerFixture{
		assembler: NewAssembler(knowledgeService, assets), sessions: sessionService, knowledge: knowledgeService, assets: assets,
	}
}

func (fixture assemblerFixture) importAsset(t *testing.T, name, mediaType string, contents []byte) asset.Asset {
	t.Helper()
	created, err := fixture.assets.Import(asset.ImportInput{
		Reader: bytes.NewReader(contents), DisplayName: name, MediaType: mediaType, Source: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return created
}
