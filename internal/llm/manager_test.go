package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
)

func TestManagerCreatesSiblingPanelsForMultipleQuickPaths(t *testing.T) {
	firstServer := jsonProviderServer(t, http.StatusOK, `{"content":"first response"}`)
	secondServer := jsonProviderServer(t, http.StatusOK, `{"content":"second response"}`)
	fixture := newManagerFixture(t, []provider.Provider{
		testJSONProvider("provider-a", firstServer.URL), testJSONProvider("provider-b", secondServer.URL),
	}, []provider.QuickPath{
		testQuickPath("path-a", "provider-a"), testQuickPath("path-b", "provider-b"),
	})

	runs, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path-a", "path-b"})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs = %#v, error = %v", runs, err)
	}
	waitForTerminalRuns(t, fixture.manager, runs)
	workspace, _ := fixture.sessions.Get(fixture.sessionID)
	children := panelsWithParent(workspace.Panels, fixture.panelID)
	if len(children) != 2 || children[0].ParentID != fixture.panelID || children[1].ParentID != fixture.panelID {
		t.Fatalf("children = %#v", children)
	}
	if children[0].Result == nil || children[1].Result == nil || children[0].Result.RunID == children[1].Result.RunID {
		t.Fatalf("result metadata = %#v", children)
	}
	contents := map[string]bool{children[0].Content: true, children[1].Content: true}
	if !contents["first response"] || !contents["second response"] {
		t.Fatalf("result contents = %#v", contents)
	}
}

func TestManagerPersistsSnapshotBeforeRequestAndPublishesEvents(t *testing.T) {
	requestStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-release
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"content":"streamed"}`)
	}))
	defer server.Close()
	fixture := newManagerFixture(t,
		[]provider.Provider{testJSONProvider("provider", server.URL)},
		[]provider.QuickPath{testQuickPath("path", "provider")},
	)
	runs, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path"})
	if err != nil {
		t.Fatal(err)
	}
	<-requestStarted
	persisted, exists := fixture.runStore.Get(runs[0].ID)
	if !exists || persisted.Snapshot.PanelID != fixture.panelID || persisted.Snapshot.Body == nil {
		t.Fatalf("snapshot was not persisted before request: %#v", persisted)
	}
	events, unsubscribe, err := fixture.manager.Subscribe(runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer unsubscribe()
	first := <-events
	if first.Type != RunEventSnapshot || first.Run.ID != runs[0].ID {
		t.Fatalf("first event = %#v", first)
	}
	close(release)
	seenChunk := false
	seenSucceeded := false
	deadline := time.After(3 * time.Second)
	for !seenSucceeded {
		select {
		case event, open := <-events:
			if !open {
				if !seenSucceeded {
					t.Fatal("event stream closed before terminal state")
				}
				continue
			}
			if event.Type == RunEventChunk && event.Chunk == "streamed" {
				seenChunk = true
			}
			if event.Type == RunEventState && event.Run.State == RunSucceeded {
				seenSucceeded = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for run events")
		}
	}
	if !seenChunk {
		t.Fatal("chunk event was not published")
	}
}

func TestManagerCancellationDoesNotCreateSuccessPanel(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		select {
		case <-request.Context().Done():
		case <-releaseHandler:
		}
	}))
	defer server.Close()
	defer close(releaseHandler)
	fixture := newManagerFixture(t,
		[]provider.Provider{testJSONProvider("provider", server.URL)},
		[]provider.QuickPath{testQuickPath("path", "provider")},
	)
	runs, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path"})
	if err != nil {
		t.Fatal(err)
	}
	<-requestStarted
	if err := fixture.manager.Cancel(runs[0].ID); err != nil {
		t.Fatal(err)
	}
	waitForTerminalRuns(t, fixture.manager, runs)
	got, _ := fixture.manager.Get(runs[0].ID)
	if got.State != RunCancelled || got.Error.Code != "cancelled" {
		t.Fatalf("cancelled run = %#v", got)
	}
	workspace, _ := fixture.sessions.Get(fixture.sessionID)
	if children := panelsWithParent(workspace.Panels, fixture.panelID); len(children) != 0 {
		t.Fatalf("cancelled run created panels: %#v", children)
	}
}

func TestManagerFailedRequestCreatesNoPanelAndShutdownWaits(t *testing.T) {
	t.Run("failed request", func(t *testing.T) {
		server := jsonProviderServer(t, http.StatusBadGateway, "failure")
		fixture := newManagerFixture(t,
			[]provider.Provider{testJSONProvider("provider", server.URL)},
			[]provider.QuickPath{testQuickPath("path", "provider")},
		)
		runs, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path"})
		if err != nil {
			t.Fatal(err)
		}
		waitForTerminalRuns(t, fixture.manager, runs)
		got, _ := fixture.manager.Get(runs[0].ID)
		if got.State != RunFailed || got.Error.Code == "" {
			t.Fatalf("failed run = %#v", got)
		}
		workspace, _ := fixture.sessions.Get(fixture.sessionID)
		if children := panelsWithParent(workspace.Panels, fixture.panelID); len(children) != 0 {
			t.Fatalf("failed run created panels: %#v", children)
		}
	})

	t.Run("shutdown", func(t *testing.T) {
		requestStarted := make(chan struct{})
		releaseHandler := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			close(requestStarted)
			select {
			case <-request.Context().Done():
			case <-releaseHandler:
			}
		}))
		defer server.Close()
		defer close(releaseHandler)
		fixture := newManagerFixture(t,
			[]provider.Provider{testJSONProvider("provider", server.URL)},
			[]provider.QuickPath{testQuickPath("path", "provider")},
		)
		if _, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path"}); err != nil {
			t.Fatal(err)
		}
		<-requestStarted
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := fixture.manager.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path"}); !errors.Is(err, ErrManagerClosed) {
			t.Fatalf("start after shutdown error = %v", err)
		}
	})
}

func TestManagerRejectsDuplicateAndDisabledQuickPaths(t *testing.T) {
	server := jsonProviderServer(t, http.StatusOK, `{"content":"ok"}`)
	disabled := testJSONProvider("provider", server.URL)
	disabled.Enabled = false
	fixture := newManagerFixture(t, []provider.Provider{disabled}, []provider.QuickPath{testQuickPath("path", "provider")})
	if _, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path", "path"}); err == nil {
		t.Fatal("duplicate quick paths were accepted")
	}
	if _, err := fixture.manager.Start(fixture.sessionID, fixture.panelID, []string{"path"}); err == nil {
		t.Fatal("disabled provider was accepted")
	}
}

type managerFixture struct {
	manager   *Manager
	sessions  *session.Service
	runStore  *RunStore
	sessionID string
	panelID   string
}

func newManagerFixture(t *testing.T, providers []provider.Provider, quickPaths []provider.QuickPath) managerFixture {
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
	sessionRepository, err := session.OpenRepository(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(sessionRepository, assets)
	workspace, err := sessions.CreateSession(session.CreateSessionInput{Title: "manager test"})
	if err != nil {
		t.Fatal(err)
	}
	configRepository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	llmConfig := configRepository.Snapshot().LLM
	llmConfig.Providers = providers
	llmConfig.QuickPaths = quickPaths
	if _, err := configRepository.UpdateLLM(llmConfig); err != nil {
		t.Fatal(err)
	}
	runStore, err := OpenRunStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	manager := NewManager(configRepository, sessions, NewAssembler(knowledgeService, assets), provider.Executor{}, runStore)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return managerFixture{
		manager: manager, sessions: sessions, runStore: runStore,
		sessionID: workspace.Session.ID, panelID: workspace.Panels[0].ID,
	}
}

func testJSONProvider(id, url string) provider.Provider {
	item := provider.DefaultLLMConfig().Providers[0]
	item.ID = id
	item.Name = id
	item.URL = url
	item.BodyTemplate = `{"prompt":${CONTENT_JSON}}`
	item.ResponseMode = provider.ResponseModeJSON
	item.ResponseContentPath = "content"
	item.StreamContentPath = ""
	item.StreamDonePath = ""
	return item
}

func testQuickPath(id, providerID string) provider.QuickPath {
	return provider.QuickPath{ID: id, Name: id, ProviderID: providerID, Params: json.RawMessage(`{}`)}
}

func jsonProviderServer(t *testing.T, status int, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, response)
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForTerminalRuns(t *testing.T, manager *Manager, runs []Run) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		allTerminal := true
		for _, run := range runs {
			current, exists := manager.Get(run.ID)
			if !exists || !current.State.terminal() {
				allTerminal = false
				break
			}
		}
		if allTerminal {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("runs did not become terminal: %#v", runs)
}

func panelsWithParent(panels []session.Panel, parentID string) []session.Panel {
	result := make([]session.Panel, 0)
	for _, panel := range panels {
		if panel.ParentID == parentID {
			result = append(result, panel)
		}
	}
	return result
}
