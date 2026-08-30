package llm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/session"
)

func TestExaServiceCreatesFormattedChildPanelOnlyOnSuccess(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `{"results":[{"title":"Result"}]}`)
		}))
		defer server.Close()
		fixture := newExaServiceFixture(t, server.URL)
		panel := fixture.setCandidate(t, `{"tool":"exa.search","arguments":{"query":"golang","num_results":3}}`)
		created, err := fixture.service.Execute(context.Background(), fixture.sessionID, panel.ID)
		if err != nil {
			t.Fatal(err)
		}
		if created.ParentID != panel.ID || created.Title != "Exa: golang" || !created.Included ||
			!strings.Contains(created.Content, "\n  \"results\"") || created.Result == nil || created.Result.Source != "exa" ||
			!strings.Contains(created.Result.RequestSummary, "num_results=3") {
			t.Fatalf("created panel = %#v", created)
		}
	})

	t.Run("failure", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "failure")
		}))
		defer server.Close()
		fixture := newExaServiceFixture(t, server.URL)
		panel := fixture.setCandidate(t, `{"tool":"exa.search","arguments":{"query":"golang"}}`)
		if _, err := fixture.service.Execute(context.Background(), fixture.sessionID, panel.ID); err == nil {
			t.Fatal("Exa failure was accepted")
		}
		workspace, _ := fixture.sessions.Get(fixture.sessionID)
		if children := panelsWithParent(workspace.Panels, panel.ID); len(children) != 0 {
			t.Fatalf("failed Exa request created panels: %#v", children)
		}
	})
}

func TestExaServiceRejectsNonCandidatePanel(t *testing.T) {
	fixture := newExaServiceFixture(t, "http://127.0.0.1:1")
	panel := fixture.setCandidate(t, "ordinary panel text")
	if _, err := fixture.service.Execute(context.Background(), fixture.sessionID, panel.ID); err == nil {
		t.Fatal("ordinary panel was accepted as an Exa request")
	}
}

type exaServiceFixture struct {
	service   *ExaService
	sessions  *session.Service
	sessionID string
	panelID   string
}

func newExaServiceFixture(t *testing.T, apiURL string) exaServiceFixture {
	t.Helper()
	root := t.TempDir()
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	repository, err := session.OpenRepository(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(repository, assets)
	workspace, err := sessions.CreateSession(session.CreateSessionInput{Title: "Exa"})
	if err != nil {
		t.Fatal(err)
	}
	configRepository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	llmConfiguration := configRepository.Snapshot().LLM
	llmConfiguration.Exa.APIURL = apiURL
	llmConfiguration.Exa.APIKey = "exa-key"
	if _, err := configRepository.UpdateLLM(llmConfiguration); err != nil {
		t.Fatal(err)
	}
	return exaServiceFixture{
		service: NewExaService(configRepository, sessions, exa.Client{}), sessions: sessions,
		sessionID: workspace.Session.ID, panelID: workspace.Panels[0].ID,
	}
}

func (fixture exaServiceFixture) setCandidate(t *testing.T, content string) session.Panel {
	t.Helper()
	updated, err := fixture.sessions.UpdatePanel(fixture.sessionID, fixture.panelID, session.UpdatePanelInput{Content: content, Included: true})
	if err != nil {
		t.Fatal(err)
	}
	return updated
}
