package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"unicode"
)

const (
	ResponseModeJSON    = "json"
	ResponseModeSSEJSON = "sse_json"

	defaultConnectTimeoutSeconds = 10
	defaultTotalTimeoutSeconds   = 600
	defaultMaxResponseBytes      = int64(16 << 20)
	defaultMaxAssetBytes         = int64(32 << 20)
	maximumHTTPBytes             = int64(1 << 30)
)

type Provider struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	URL                   string            `json:"url"`
	Method                string            `json:"method"`
	APIKey                string            `json:"api_key,omitempty"`
	Headers               map[string]string `json:"headers"`
	BodyTemplate          string            `json:"body_template"`
	ResponseMode          string            `json:"response_mode"`
	ResponseContentPath   string            `json:"response_content_path,omitempty"`
	StreamContentPath     string            `json:"stream_content_path,omitempty"`
	StreamDonePath        string            `json:"stream_done_path,omitempty"`
	ConnectTimeoutSeconds int               `json:"connect_timeout_seconds"`
	TotalTimeoutSeconds   int               `json:"total_timeout_seconds"`
	MaxResponseBytes      int64             `json:"max_response_bytes"`
	MaxAssetBytes         int64             `json:"max_asset_bytes"`
	Enabled               bool              `json:"enabled"`
}

type QuickPath struct {
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	ProviderID string          `json:"provider_id"`
	Model      string          `json:"model"`
	Params     json.RawMessage `json:"params"`
}

type PromptTemplate struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

type ExaConfig struct {
	APIURL           string `json:"api_url"`
	APIKey           string `json:"api_key,omitempty"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
	MaxResponseBytes int64  `json:"max_response_bytes"`
}

type LLMConfig struct {
	Providers       []Provider       `json:"providers"`
	QuickPaths      []QuickPath      `json:"quick_paths"`
	PromptTemplates []PromptTemplate `json:"prompt_templates"`
	Exa             ExaConfig        `json:"exa"`
}

func DefaultLLMConfig() LLMConfig {
	local := defaultLlamaCompletionProvider()
	return LLMConfig{
		Providers: []Provider{local},
		QuickPaths: []QuickPath{{
			ID: "local", Name: "Local", ProviderID: local.ID, Params: json.RawMessage(`{}`),
		}},
		PromptTemplates: []PromptTemplate{},
		Exa: ExaConfig{
			APIURL: "https://api.exa.ai/search", TimeoutSeconds: 60, MaxResponseBytes: defaultMaxResponseBytes,
		},
	}
}

func (configuration *LLMConfig) AddLlamaCompletionPreset() error {
	if configuration == nil {
		return fmt.Errorf("LLM config is required")
	}
	for _, item := range configuration.Providers {
		if item.ID == "llama-local" {
			return fmt.Errorf("llama completion preset already exists")
		}
	}
	for _, item := range configuration.QuickPaths {
		if item.ID == "local" {
			return fmt.Errorf("llama completion preset already exists")
		}
	}
	candidate := configuration.Clone()
	candidate.Providers = append(candidate.Providers, defaultLlamaCompletionProvider())
	candidate.QuickPaths = append(candidate.QuickPaths, QuickPath{
		ID: "local", Name: "Local", ProviderID: "llama-local", Params: json.RawMessage(`{}`),
	})
	if err := candidate.Validate(); err != nil {
		return err
	}
	*configuration = candidate
	return nil
}

func (configuration LLMConfig) Validate() error {
	providerIDs := make(map[string]struct{}, len(configuration.Providers))
	for index, item := range configuration.Providers {
		if err := validateID(item.ID); err != nil {
			return fmt.Errorf("provider %d ID: %w", index, err)
		}
		if _, duplicate := providerIDs[item.ID]; duplicate {
			return fmt.Errorf("duplicate provider ID %q", item.ID)
		}
		providerIDs[item.ID] = struct{}{}
		if err := item.validate(); err != nil {
			return fmt.Errorf("provider %q: %w", item.ID, err)
		}
	}

	quickPathIDs := make(map[string]struct{}, len(configuration.QuickPaths))
	for index, item := range configuration.QuickPaths {
		if err := validateID(item.ID); err != nil {
			return fmt.Errorf("quick path %d ID: %w", index, err)
		}
		if _, duplicate := quickPathIDs[item.ID]; duplicate {
			return fmt.Errorf("duplicate quick path ID %q", item.ID)
		}
		quickPathIDs[item.ID] = struct{}{}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("quick path %q name is required", item.ID)
		}
		if _, exists := providerIDs[item.ProviderID]; !exists {
			return fmt.Errorf("quick path %q references unknown provider %q", item.ID, item.ProviderID)
		}
		if err := validateJSONObject(item.Params); err != nil {
			return fmt.Errorf("quick path %q params must be one JSON object: %w", item.ID, err)
		}
	}

	templateIDs := make(map[string]struct{}, len(configuration.PromptTemplates))
	for index, item := range configuration.PromptTemplates {
		if err := validateID(item.ID); err != nil {
			return fmt.Errorf("prompt template %d ID: %w", index, err)
		}
		if _, duplicate := templateIDs[item.ID]; duplicate {
			return fmt.Errorf("duplicate prompt template ID %q", item.ID)
		}
		templateIDs[item.ID] = struct{}{}
		if strings.TrimSpace(item.Name) == "" {
			return fmt.Errorf("prompt template %q name is required", item.ID)
		}
	}
	if err := configuration.Exa.validate(); err != nil {
		return fmt.Errorf("Exa config: %w", err)
	}
	return nil
}

func (configuration LLMConfig) Clone() LLMConfig {
	clone := configuration
	clone.Providers = make([]Provider, len(configuration.Providers))
	for index, item := range configuration.Providers {
		clone.Providers[index] = item
		clone.Providers[index].Headers = cloneStringMap(item.Headers)
	}
	clone.QuickPaths = make([]QuickPath, len(configuration.QuickPaths))
	for index, item := range configuration.QuickPaths {
		clone.QuickPaths[index] = item
		clone.QuickPaths[index].Params = append(json.RawMessage{}, item.Params...)
	}
	clone.PromptTemplates = append([]PromptTemplate{}, configuration.PromptTemplates...)
	return clone
}

func defaultLlamaCompletionProvider() Provider {
	return Provider{
		ID: "llama-local", Name: "llama.cpp Local", URL: "http://127.0.0.1:8080/completion", Method: "POST",
		Headers:      map[string]string{"Content-Type": "application/json"},
		BodyTemplate: `{"prompt":${CONTENT_JSON},"stream":true}`,
		ResponseMode: ResponseModeSSEJSON, StreamContentPath: "content", StreamDonePath: "stop",
		ConnectTimeoutSeconds: defaultConnectTimeoutSeconds, TotalTimeoutSeconds: defaultTotalTimeoutSeconds,
		MaxResponseBytes: defaultMaxResponseBytes, MaxAssetBytes: defaultMaxAssetBytes, Enabled: true,
	}
}

func (item Provider) validate() error {
	if strings.TrimSpace(item.Name) == "" {
		return fmt.Errorf("name is required")
	}
	parsed, err := url.Parse(item.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("URL must be absolute HTTP(S)")
	}
	if item.Method != "POST" {
		return fmt.Errorf("method must be POST")
	}
	for name, value := range item.Headers {
		if !validHeaderName(name) {
			return fmt.Errorf("invalid header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a newline", name)
		}
		if remaining := strings.ReplaceAll(value, "${API_KEY}", ""); strings.Contains(remaining, "${") {
			return fmt.Errorf("header %q contains an unknown template variable", name)
		}
	}
	if err := validateBodyTemplate(item.BodyTemplate); err != nil {
		return err
	}
	switch item.ResponseMode {
	case ResponseModeJSON:
		if strings.TrimSpace(item.ResponseContentPath) == "" {
			return fmt.Errorf("response content path is required for JSON mode")
		}
	case ResponseModeSSEJSON:
		if strings.TrimSpace(item.StreamContentPath) == "" {
			return fmt.Errorf("stream content path is required for SSE JSON mode")
		}
	default:
		return fmt.Errorf("unsupported response mode %q", item.ResponseMode)
	}
	if item.ConnectTimeoutSeconds < 1 || item.ConnectTimeoutSeconds > 300 {
		return fmt.Errorf("connect timeout must be between 1 and 300 seconds")
	}
	if item.TotalTimeoutSeconds < 1 || item.TotalTimeoutSeconds > 86400 {
		return fmt.Errorf("total timeout must be between 1 and 86400 seconds")
	}
	if item.MaxResponseBytes < 1 || item.MaxResponseBytes > maximumHTTPBytes {
		return fmt.Errorf("max response bytes must be between 1 and %d", maximumHTTPBytes)
	}
	if item.MaxAssetBytes < 1 || item.MaxAssetBytes > maximumHTTPBytes {
		return fmt.Errorf("max asset bytes must be between 1 and %d", maximumHTTPBytes)
	}
	return nil
}

func (configuration ExaConfig) validate() error {
	parsed, err := url.Parse(configuration.APIURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("API URL must be absolute HTTP(S)")
	}
	if configuration.TimeoutSeconds < 1 || configuration.TimeoutSeconds > 300 {
		return fmt.Errorf("timeout must be between 1 and 300 seconds")
	}
	if configuration.MaxResponseBytes < 1 || configuration.MaxResponseBytes > maximumHTTPBytes {
		return fmt.Errorf("max response bytes must be between 1 and %d", maximumHTTPBytes)
	}
	return nil
}

func validateID(id string) error {
	if id == "" || len(id) > 120 {
		return fmt.Errorf("must contain 1 to 120 characters")
	}
	for _, character := range id {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || strings.ContainsRune("._-", character) {
			continue
		}
		return fmt.Errorf("contains unsupported character %q", character)
	}
	return nil
}

func validateBodyTemplate(template string) error {
	replaced := template
	for _, variable := range []string{
		"${CONTENT_JSON}", "${PANELS_JSON}", "${KNOWLEDGE_JSON}", "${ASSET_DATA_URLS_JSON}", "${MODEL_JSON}", "${PARAMS_JSON}",
	} {
		replaced = strings.ReplaceAll(replaced, variable, "null")
	}
	if strings.Contains(replaced, "${") {
		return fmt.Errorf("body contains an unknown template variable")
	}
	if err := validateJSONObject(json.RawMessage(replaced)); err != nil {
		return fmt.Errorf("body template must produce one JSON object: %w", err)
	}
	return nil
}

func validateJSONObject(contents json.RawMessage) error {
	if len(bytes.TrimSpace(contents)) == 0 {
		return fmt.Errorf("empty JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return fmt.Errorf("multiple JSON values")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("top-level value is not a JSON object")
	}
	return nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for index := 0; index < len(name); index++ {
		character := name[index]
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			continue
		}
		return false
	}
	return true
}

func cloneStringMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
