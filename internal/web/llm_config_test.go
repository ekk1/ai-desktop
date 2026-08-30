package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ekk1/ai-desktop/internal/asset"
	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/session"
)

func TestLLMConfigAPIUpdatesStrictConfigurationAndAddsPreset(t *testing.T) {
	fixture := newLLMWebFixture(t)
	recorder := fixture.request(t, http.MethodGet, "/api/v1/llm/config", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET config status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	configuration := fixture.config.Snapshot().LLM
	configuration.Exa.APIKey = "exa-test"
	encoded, _ := json.Marshal(configuration)
	recorder = fixture.request(t, http.MethodPut, "/api/v1/llm/config", encoded)
	if recorder.Code != http.StatusOK || fixture.config.Snapshot().LLM.Exa.APIKey != "exa-test" {
		t.Fatalf("PUT config status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = fixture.request(t, http.MethodPut, "/api/v1/llm/config", []byte(`{"providers":[],"quick_paths":[],"prompt_templates":[],"exa":{"api_url":"https://api.exa.ai/search","timeout_seconds":60,"max_response_bytes":16777216},"unknown":true}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	configuration = fixture.config.Snapshot().LLM
	configuration.Providers = nil
	configuration.QuickPaths = nil
	encoded, _ = json.Marshal(configuration)
	recorder = fixture.request(t, http.MethodPut, "/api/v1/llm/config", encoded)
	if recorder.Code != http.StatusOK {
		t.Fatalf("clear presets status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = fixture.request(t, http.MethodPost, "/api/v1/llm/providers/preset/llama-completion", []byte(`{}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("preset status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	got := fixture.config.Snapshot().LLM
	if len(got.Providers) != 1 || got.Providers[0].ID != "llama-local" || len(got.QuickPaths) != 1 {
		t.Fatalf("preset config = %#v", got)
	}
}

type llmWebFixture struct {
	handler  http.Handler
	config   *config.Repository
	sessions *session.Service
}

func newLLMWebFixture(t *testing.T) llmWebFixture {
	t.Helper()
	root := t.TempDir()
	configRepository, err := config.OpenRepository(filepath.Join(root, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	assets, err := asset.OpenRepository(filepath.Join(root, "assets", "index.json"), filepath.Join(root, "assets", "files"))
	if err != nil {
		t.Fatal(err)
	}
	sessionRepository, err := session.OpenRepository(filepath.Join(root, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.NewService(sessionRepository, assets)
	handler := NewHandler(Options{
		Version: "test", DataDir: root, Config: configRepository.Snapshot(),
		ConfigRepository: configRepository, SessionService: sessions,
	})
	return llmWebFixture{handler: handler, config: configRepository, sessions: sessions}
}

func (fixture llmWebFixture) request(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	fixture.handler.ServeHTTP(recorder, request)
	return recorder
}
