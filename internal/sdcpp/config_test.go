package sdcpp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultImageConfigContainsRunnableLocalProvider(t *testing.T) {
	configuration := DefaultImageConfig()
	if err := configuration.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(configuration.Providers) != 1 {
		t.Fatalf("providers = %#v", configuration.Providers)
	}
	got := configuration.Providers[0]
	if got.ID != "sdcpp-local" || got.BaseURL != "http://127.0.0.1:1234" || got.MaxConcurrentJobs != 1 {
		t.Fatalf("provider = %#v", got)
	}
	if got.ConnectTimeoutSeconds != 10 || got.JobTimeoutSeconds != 3600 || got.PollIntervalMilliseconds != 750 {
		t.Fatalf("timeouts = %#v", got)
	}
}

func TestDefaultImageParamsReturnsIndependentObjects(t *testing.T) {
	first := DefaultImageParams()
	second := DefaultImageParams()
	first[0] = 'X'
	if string(second) != `{"width":1024,"height":1024,"seed":-1,"batch_count":1,"output_format":"png"}` {
		t.Fatalf("params = %s", second)
	}
	var object map[string]any
	if err := json.Unmarshal(second, &object); err != nil {
		t.Fatal(err)
	}
}

func TestImageConfigRejectsInvalidLimitsAndHeaderInjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ImageProvider)
		want   string
	}{
		{name: "header newline", mutate: func(item *ImageProvider) { item.Headers["X-Test"] = "ok\nInjected: yes" }, want: "newline"},
		{name: "short polling", mutate: func(item *ImageProvider) { item.PollIntervalMilliseconds = 99 }, want: "poll"},
		{name: "long polling", mutate: func(item *ImageProvider) { item.PollIntervalMilliseconds = 10001 }, want: "poll"},
		{name: "zero connect timeout", mutate: func(item *ImageProvider) { item.ConnectTimeoutSeconds = 0 }, want: "connect"},
		{name: "long job timeout", mutate: func(item *ImageProvider) { item.JobTimeoutSeconds = 86401 }, want: "job"},
		{name: "zero response limit", mutate: func(item *ImageProvider) { item.MaxResponseBytes = 0 }, want: "response"},
		{name: "large image limit", mutate: func(item *ImageProvider) { item.MaxImageBytes = (1 << 30) + 1 }, want: "image"},
		{name: "zero concurrency", mutate: func(item *ImageProvider) { item.MaxConcurrentJobs = 0 }, want: "concurrent"},
		{name: "large concurrency", mutate: func(item *ImageProvider) { item.MaxConcurrentJobs = 17 }, want: "concurrent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := DefaultImageConfig()
			test.mutate(&configuration.Providers[0])
			err := configuration.Validate()
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestImageConfigRejectsBrokenProviderIdentityAndURL(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ImageConfig)
	}{
		{name: "empty ID", mutate: func(configuration *ImageConfig) { configuration.Providers[0].ID = "" }},
		{name: "unsafe ID", mutate: func(configuration *ImageConfig) { configuration.Providers[0].ID = "../gpu" }},
		{name: "empty name", mutate: func(configuration *ImageConfig) { configuration.Providers[0].Name = " " }},
		{name: "relative URL", mutate: func(configuration *ImageConfig) { configuration.Providers[0].BaseURL = "/sdcpp" }},
		{name: "query URL", mutate: func(configuration *ImageConfig) { configuration.Providers[0].BaseURL = "http://127.0.0.1:1234?x=1" }},
		{name: "fragment URL", mutate: func(configuration *ImageConfig) { configuration.Providers[0].BaseURL = "http://127.0.0.1:1234#x" }},
		{name: "trailing slash", mutate: func(configuration *ImageConfig) { configuration.Providers[0].BaseURL += "/" }},
		{name: "duplicate ID", mutate: func(configuration *ImageConfig) {
			configuration.Providers = append(configuration.Providers, configuration.Providers[0])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configuration := DefaultImageConfig()
			test.mutate(&configuration)
			if err := configuration.Validate(); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
}

func TestImageConfigCloneDoesNotAliasHeaders(t *testing.T) {
	original := DefaultImageConfig()
	clone := original.Clone()
	clone.Providers[0].Headers["X-Test"] = "changed"
	if original.Providers[0].Headers["X-Test"] != "" {
		t.Fatalf("original = %#v", original)
	}
}
