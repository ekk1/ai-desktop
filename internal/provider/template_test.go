package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRenderJSONEncodesContentAndMergesQuickParams(t *testing.T) {
	provider := DefaultLLMConfig().Providers[0]
	provider.BodyTemplate = `{"prompt":${CONTENT_JSON},"stream":true,"model":${MODEL_JSON}}`
	quickPath := QuickPath{Model: "model-a", Params: json.RawMessage(`{"temperature":0.2,"stream":false}`)}
	got, err := Render(provider, quickPath, TemplateVariables{Content: "quote \" and newline\n"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body["prompt"] != "quote \" and newline\n" || body["model"] != "model-a" || body["temperature"] != 0.2 || body["stream"] != false {
		t.Fatalf("body = %#v", body)
	}
}

func TestRenderRejectsPlaceholderInsideJSONStringAndHeaderNewline(t *testing.T) {
	provider := DefaultLLMConfig().Providers[0]
	provider.BodyTemplate = `{"prompt":"${CONTENT_JSON}"}`
	if _, err := Render(provider, QuickPath{Params: json.RawMessage(`{}`)}, TemplateVariables{Content: "x"}); !errors.Is(err, ErrPlaceholderPosition) {
		t.Fatalf("placeholder position error = %v", err)
	}

	provider = DefaultLLMConfig().Providers[0]
	provider.Headers = map[string]string{"X-Test": "${API_KEY}\nInjected"}
	if _, err := Render(provider, QuickPath{Params: json.RawMessage(`{}`)}, TemplateVariables{}); !errors.Is(err, ErrInvalidHeader) {
		t.Fatalf("invalid header error = %v", err)
	}
}

func TestRenderExpandsAndRedactsAPIKeyHeaders(t *testing.T) {
	provider := DefaultLLMConfig().Providers[0]
	provider.APIKey = "secret"
	provider.Headers = map[string]string{
		"Authorization": "Bearer ${API_KEY}",
		"X-Label":       "public",
	}
	got, err := Render(provider, QuickPath{Params: json.RawMessage(`{}`)}, TemplateVariables{Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Headers["Authorization"] != "Bearer secret" || got.SnapshotHeaders["Authorization"] != "<redacted>" || got.SnapshotHeaders["X-Label"] != "public" {
		t.Fatalf("headers = %#v, snapshot = %#v", got.Headers, got.SnapshotHeaders)
	}
	if strings.Contains(string(got.Body), "secret") {
		t.Fatalf("API key leaked into body: %s", got.Body)
	}
}
