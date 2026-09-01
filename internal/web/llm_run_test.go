package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/knowledge"
	"github.com/ekk1/ai-desktop/internal/llm"
	"github.com/ekk1/ai-desktop/internal/provider"
	"github.com/ekk1/ai-desktop/internal/session"
)

func TestLLMRunAPIExecutesStreamsAndCancelsIdempotently(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseProvider
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: {\"content\":\"hel\",\"stop\":false}\n\ndata: {\"content\":\"lo\",\"stop\":true}\n\n")
	}))
	defer providerServer.Close()
	fixture := newLLMExecutionWebFixture(t, providerServer.URL, "http://127.0.0.1:1")
	body := []byte(`{"panel_id":"` + fixture.panelID + `","quick_path_ids":["local"]}`)
	execute := fixture.request(http.MethodPost, "/api/v1/llm/sessions/"+fixture.sessionID+"/execute", body)
	if execute.Code != http.StatusAccepted {
		t.Fatalf("execute status = %d, body = %s", execute.Code, execute.Body.String())
	}
	var accepted struct {
		Runs []llm.Run `json:"runs"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); err != nil || len(accepted.Runs) != 1 {
		t.Fatalf("accepted = %#v, error = %v", accepted, err)
	}
	<-requestStarted
	eventsDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		eventsDone <- fixture.request(http.MethodGet, "/api/v1/llm/runs/"+accepted.Runs[0].ID+"/events", nil)
	}()
	time.Sleep(20 * time.Millisecond)
	close(releaseProvider)
	var events *httptest.ResponseRecorder
	select {
	case events = <-eventsDone:
	case <-time.After(3 * time.Second):
		t.Fatal("run event stream did not finish")
	}
	if events.Code != http.StatusOK || !strings.HasPrefix(events.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events status = %d, headers = %#v", events.Code, events.Header())
	}
	for _, eventType := range []string{"event: snapshot", "event: chunk", "event: state"} {
		if !strings.Contains(events.Body.String(), eventType) {
			t.Fatalf("event stream does not contain %q: %s", eventType, events.Body.String())
		}
	}

	cancel := fixture.request(http.MethodPost, "/api/v1/llm/runs/"+accepted.Runs[0].ID+"/cancel", []byte(`{}`))
	if cancel.Code != http.StatusOK {
		t.Fatalf("completed cancel status = %d, body = %s", cancel.Code, cancel.Body.String())
	}
	get := fixture.request(http.MethodGet, "/api/v1/llm/runs/"+accepted.Runs[0].ID, nil)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"state":"succeeded"`) {
		t.Fatalf("get run status = %d, body = %s", get.Code, get.Body.String())
	}
}

func TestLLMSessionDeleteRejectsActiveRun(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseProvider := make(chan struct{})
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		select {
		case <-releaseProvider:
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"content":"done"}`)
		case <-request.Context().Done():
		}
	}))
	defer providerServer.Close()
	defer close(releaseProvider)
	fixture := newLLMExecutionWebFixture(t, providerServer.URL, "http://127.0.0.1:1")
	execute := fixture.request(http.MethodPost, "/api/v1/llm/sessions/"+fixture.sessionID+"/execute", []byte(`{"panel_id":"`+fixture.panelID+`","quick_path_ids":["local"]}`))
	if execute.Code != http.StatusAccepted {
		t.Fatalf("execute status = %d: %s", execute.Code, execute.Body.String())
	}
	<-requestStarted

	deleted := fixture.request(http.MethodDelete, "/api/v1/llm/sessions/"+fixture.sessionID, nil)
	if deleted.Code != http.StatusConflict || !strings.Contains(deleted.Body.String(), `"code":"active_runs"`) {
		t.Fatalf("delete active session = %d: %s", deleted.Code, deleted.Body.String())
	}
	if _, exists := fixture.sessions.Get(fixture.sessionID); !exists {
		t.Fatal("active session was deleted")
	}
}

func TestLLMSessionDeletePurgesCompletedRuns(t *testing.T) {
	providerServer := jsonProviderServerForWeb(t, `{"content":"done"}`)
	fixture := newLLMExecutionWebFixture(t, providerServer.URL, "http://127.0.0.1:1")
	execute := fixture.request(http.MethodPost, "/api/v1/llm/sessions/"+fixture.sessionID+"/execute", []byte(`{"panel_id":"`+fixture.panelID+`","quick_path_ids":["local"]}`))
	if execute.Code != http.StatusAccepted {
		t.Fatalf("execute status = %d: %s", execute.Code, execute.Body.String())
	}
	var accepted struct {
		Runs []llm.Run `json:"runs"`
	}
	if err := json.NewDecoder(execute.Body).Decode(&accepted); err != nil || len(accepted.Runs) != 1 {
		t.Fatalf("accepted = %#v, error = %v", accepted, err)
	}
	waitForWebRunTerminal(t, fixture.manager, accepted.Runs[0].ID)

	deleted := fixture.request(http.MethodDelete, "/api/v1/llm/sessions/"+fixture.sessionID, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete completed session = %d: %s", deleted.Code, deleted.Body.String())
	}
	get := fixture.request(http.MethodGet, "/api/v1/llm/runs/"+accepted.Runs[0].ID, nil)
	if get.Code != http.StatusNotFound {
		t.Fatalf("deleted session run status = %d: %s", get.Code, get.Body.String())
	}
}

type llmExecutionWebFixture struct {
	handler   http.Handler
	manager   *llm.Manager
	sessions  *session.Service
	sessionID string
	panelID   string
}

func newLLMExecutionWebFixture(t *testing.T, providerURL, exaURL string) llmExecutionWebFixture {
	t.Helper()
	root := t.TempDir()
	configRepository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	configuration := configRepository.Snapshot().LLM
	configuration.Providers[0].URL = providerURL
	configuration.Exa.APIURL = exaURL
	configuration.Exa.APIKey = "exa-key"
	if _, err := configRepository.UpdateLLM(configuration); err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	notes, err := knowledge.OpenRepository(filepath.Join(root, "knowledge", "notes.json"))
	if err != nil {
		t.Fatal(err)
	}
	knowledgeService := knowledge.NewService(notes, assets)
	sessionRepository, err := session.OpenRepository(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(sessionRepository, assets)
	workspace, err := sessions.CreateSession(session.CreateSessionInput{Title: "Web"})
	if err != nil {
		t.Fatal(err)
	}
	runStore, err := llm.OpenRunStore(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	manager := llm.NewManager(configRepository, sessions, llm.NewAssembler(knowledgeService, assets), provider.Executor{}, runStore)
	exaService := llm.NewExaService(configRepository, sessions, exa.Client{})
	handler := NewHandler(Options{
		Version: "test", DataDir: root, Config: configRepository.Snapshot(), ConfigRepository: configRepository,
		SessionService: sessions, LLMManager: manager, ExaService: exaService,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(ctx)
	})
	return llmExecutionWebFixture{
		handler: handler, manager: manager, sessions: sessions,
		sessionID: workspace.Session.ID, panelID: workspace.Panels[0].ID,
	}
}

func (fixture llmExecutionWebFixture) request(method, path string, body []byte) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	return recorder
}

func jsonProviderServerForWeb(t *testing.T, response string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, response)
	}))
	t.Cleanup(server.Close)
	return server
}

func waitForWebRunTerminal(t *testing.T, manager *llm.Manager, runID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		current, exists := manager.Get(runID)
		if exists && (current.State == llm.RunSucceeded || current.State == llm.RunFailed || current.State == llm.RunCancelled || current.State == llm.RunInterrupted) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s did not become terminal", runID)
}
