package sdcpp

import (
	"encoding/json"
	"fmt"
	"net/textproto"
	"net/url"
	"strings"
)

const maximumImageHTTPBytes = int64(1 << 30)

var defaultImageParams = []byte(`{"width":1024,"height":1024,"seed":-1,"batch_count":1,"output_format":"png"}`)

type ImageProvider struct {
	ID                       string            `json:"id"`
	Name                     string            `json:"name"`
	BaseURL                  string            `json:"base_url"`
	Headers                  map[string]string `json:"headers"`
	ConnectTimeoutSeconds    int               `json:"connect_timeout_seconds"`
	JobTimeoutSeconds        int               `json:"job_timeout_seconds"`
	PollIntervalMilliseconds int               `json:"poll_interval_milliseconds"`
	MaxResponseBytes         int64             `json:"max_response_bytes"`
	MaxImageBytes            int64             `json:"max_image_bytes"`
	MaxConcurrentJobs        int               `json:"max_concurrent_jobs"`
	Enabled                  bool              `json:"enabled"`
}

type ImageConfig struct {
	Providers []ImageProvider `json:"providers"`
}

func DefaultImageConfig() ImageConfig {
	return ImageConfig{Providers: []ImageProvider{{
		ID: "sdcpp-local", Name: "stable-diffusion.cpp Local", BaseURL: "http://127.0.0.1:1234",
		Headers: map[string]string{}, ConnectTimeoutSeconds: 10, JobTimeoutSeconds: 3600,
		PollIntervalMilliseconds: 750, MaxResponseBytes: 256 << 20, MaxImageBytes: 128 << 20,
		MaxConcurrentJobs: 1, Enabled: true,
	}}}
}

func DefaultImageParams() json.RawMessage {
	return append(json.RawMessage{}, defaultImageParams...)
}

func (configuration ImageConfig) Validate() error {
	seen := make(map[string]struct{}, len(configuration.Providers))
	for index, provider := range configuration.Providers {
		if err := validateImageProviderID(provider.ID); err != nil {
			return fmt.Errorf("image provider %d ID: %w", index, err)
		}
		if _, duplicate := seen[provider.ID]; duplicate {
			return fmt.Errorf("duplicate image provider ID %q", provider.ID)
		}
		seen[provider.ID] = struct{}{}
		if err := provider.validate(); err != nil {
			return fmt.Errorf("image provider %q: %w", provider.ID, err)
		}
	}
	return nil
}

func (configuration ImageConfig) Clone() ImageConfig {
	clone := configuration
	clone.Providers = make([]ImageProvider, len(configuration.Providers))
	for index, provider := range configuration.Providers {
		clone.Providers[index] = provider
		clone.Providers[index].Headers = cloneHeaders(provider.Headers)
	}
	return clone
}

func (provider ImageProvider) validate() error {
	if strings.TrimSpace(provider.Name) == "" {
		return fmt.Errorf("name is required")
	}
	parsed, err := url.Parse(provider.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("base URL must be absolute HTTP(S)")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("base URL cannot contain query or fragment")
	}
	if strings.HasSuffix(provider.BaseURL, "/") {
		return fmt.Errorf("base URL cannot have a trailing slash")
	}
	for name, value := range provider.Headers {
		if name == "" || textproto.CanonicalMIMEHeaderKey(name) == "" {
			return fmt.Errorf("invalid header name %q", name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("header %q contains a newline", name)
		}
	}
	if provider.ConnectTimeoutSeconds < 1 || provider.ConnectTimeoutSeconds > 300 {
		return fmt.Errorf("connect timeout must be between 1 and 300 seconds")
	}
	if provider.JobTimeoutSeconds < 1 || provider.JobTimeoutSeconds > 86400 {
		return fmt.Errorf("job timeout must be between 1 and 86400 seconds")
	}
	if provider.PollIntervalMilliseconds < 100 || provider.PollIntervalMilliseconds > 10000 {
		return fmt.Errorf("poll interval must be between 100 and 10000 milliseconds")
	}
	if provider.MaxResponseBytes < 1 || provider.MaxResponseBytes > maximumImageHTTPBytes {
		return fmt.Errorf("max response bytes must be between 1 and %d", maximumImageHTTPBytes)
	}
	if provider.MaxImageBytes < 1 || provider.MaxImageBytes > maximumImageHTTPBytes {
		return fmt.Errorf("max image bytes must be between 1 and %d", maximumImageHTTPBytes)
	}
	if provider.MaxConcurrentJobs < 1 || provider.MaxConcurrentJobs > 16 {
		return fmt.Errorf("max concurrent jobs must be between 1 and 16")
	}
	return nil
}

func validateImageProviderID(id string) error {
	if id == "" || len(id) > 120 {
		return fmt.Errorf("must contain 1 to 120 safe characters")
	}
	for _, character := range id {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("._-", character) {
			continue
		}
		return fmt.Errorf("contains unsafe character %q", character)
	}
	return nil
}

func cloneHeaders(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
