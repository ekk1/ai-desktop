package provider

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultLLMConfigIsValidAndRoutesLocalQuickPath(t *testing.T) {
	configuration := DefaultLLMConfig()
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Providers) != 1 || len(configuration.QuickPaths) != 1 {
		t.Fatalf("default LLM config = %#v", configuration)
	}
	local := configuration.Providers[0]
	if local.ID != "llama-local" || local.URL != "http://127.0.0.1:8080/completion" || local.ResponseMode != ResponseModeSSEJSON {
		t.Fatalf("local provider = %#v", local)
	}
	if local.StreamContentPath != "content" || local.StreamDonePath != "stop" || !strings.Contains(local.BodyTemplate, `${CONTENT_JSON}`) {
		t.Fatalf("local streaming template = %#v", local)
	}
	quickPath := configuration.QuickPaths[0]
	if quickPath.ID != "local" || quickPath.Name != "Local" || quickPath.ProviderID != local.ID {
		t.Fatalf("local quick path = %#v", quickPath)
	}
	var parameters map[string]any
	if err := json.Unmarshal(quickPath.Params, &parameters); err != nil || len(parameters) != 0 {
		t.Fatalf("local parameters = %s, %v", quickPath.Params, err)
	}
}

func TestLLMConfigRejectsBrokenReferencesAndUnsafeHeaders(t *testing.T) {
	tests := []struct {
		name   string
		change func(*LLMConfig)
		want   string
	}{
		{
			name: "missing quick path provider",
			change: func(configuration *LLMConfig) {
				configuration.QuickPaths[0].ProviderID = "missing"
			},
			want: "unknown provider",
		},
		{
			name: "header value newline",
			change: func(configuration *LLMConfig) {
				configuration.Providers[0].Headers["Authorization"] = "Bearer x\nInjected: yes"
			},
			want: "newline",
		},
		{
			name: "duplicate provider ID",
			change: func(configuration *LLMConfig) {
				configuration.Providers = append(configuration.Providers, configuration.Providers[0])
			},
			want: "duplicate provider ID",
		},
		{
			name: "params must be object",
			change: func(configuration *LLMConfig) {
				configuration.QuickPaths[0].Params = json.RawMessage(`[]`)
			},
			want: "JSON object",
		},
		{
			name: "unknown body variable",
			change: func(configuration *LLMConfig) {
				configuration.Providers[0].BodyTemplate = `{"prompt":${UNKNOWN_JSON}}`
			},
			want: "unknown template variable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := DefaultLLMConfig()
			test.change(&configuration)
			err := configuration.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestAddLlamaCompletionPresetRestoresOnlyMissingDefaults(t *testing.T) {
	configuration := LLMConfig{
		Providers:       []Provider{},
		QuickPaths:      []QuickPath{},
		PromptTemplates: []PromptTemplate{},
		Exa:             DefaultLLMConfig().Exa,
	}
	if err := configuration.AddLlamaCompletionPreset(); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Providers) != 1 || len(configuration.QuickPaths) != 1 {
		t.Fatalf("preset config = %#v", configuration)
	}
	if err := configuration.AddLlamaCompletionPreset(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate preset error = %v", err)
	}
}
