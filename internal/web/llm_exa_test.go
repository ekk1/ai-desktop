package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ekk1/ai-desktop/internal/session"
)

func TestLLMExaAPIOnlyExecutesDetectedPanel(t *testing.T) {
	exaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"results":[{"title":"Go"}]}`)
	}))
	defer exaServer.Close()
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "data: {\"content\":\"unused\",\"stop\":true}\n\n")
	}))
	defer providerServer.Close()
	fixture := newLLMExecutionWebFixture(t, providerServer.URL, exaServer.URL)
	updated, err := fixture.sessions.UpdatePanel(fixture.sessionID, fixture.panelID, session.UpdatePanelInput{
		Content: `{"tool":"exa.search","arguments":{"query":"golang"}}`, Included: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := fixture.request(http.MethodPost, "/api/v1/llm/sessions/"+fixture.sessionID+"/panels/"+updated.ID+"/exa", []byte(`{}`))
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"source":"exa"`) {
		t.Fatalf("Exa status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, err := fixture.sessions.UpdatePanel(fixture.sessionID, fixture.panelID, session.UpdatePanelInput{Content: "ordinary", Included: true}); err != nil {
		t.Fatal(err)
	}
	recorder = fixture.request(http.MethodPost, "/api/v1/llm/sessions/"+fixture.sessionID+"/panels/"+updated.ID+"/exa", []byte(`{}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("ordinary panel Exa status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
