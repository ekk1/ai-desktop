package exa

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ekk1/ai-desktop/internal/provider"
)

func TestClientSearchUsesOfficialExaFieldMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.Header.Get("x-api-key") != "exa-secret" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("request = %s %#v", request.Method, request.Header)
		}
		var body struct {
			Query      string `json:"query"`
			NumResults int    `json:"numResults"`
			Contents   struct {
				Text bool `json:"text"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Query != "golang" || body.NumResults != 8 || !body.Contents.Text {
			t.Errorf("body = %#v", body)
		}
		_, _ = io.WriteString(writer, `{"results":[{"title":"Go"}]}`)
	}))
	defer server.Close()
	configuration := provider.DefaultLLMConfig().Exa
	configuration.APIURL = server.URL
	configuration.APIKey = "exa-secret"
	got, err := (Client{}).Search(context.Background(), configuration, SearchRequest{Query: "golang", NumResults: 8})
	if err != nil || string(got) != `{"results":[{"title":"Go"}]}` {
		t.Fatalf("response = %s, error = %v", got, err)
	}
}

func TestClientSearchRejectsMissingKeyStatusLimitAndInvalidJSON(t *testing.T) {
	base := provider.DefaultLLMConfig().Exa
	if _, err := (Client{}).Search(context.Background(), base, SearchRequest{Query: "go", NumResults: 1}); !errors.Is(err, ErrMissingAPIKey) {
		t.Fatalf("missing key error = %v", err)
	}

	tests := []struct {
		name   string
		status int
		body   string
		limit  int64
		want   error
	}{
		{name: "status", status: http.StatusUnauthorized, body: strings.Repeat("denied", 2000), limit: 1 << 20, want: ErrHTTPStatus},
		{name: "limit", status: http.StatusOK, body: `{"results":[1,2,3]}`, limit: 5, want: ErrResponseLimit},
		{name: "invalid json", status: http.StatusOK, body: `not json`, limit: 1024, want: ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			configuration := base
			configuration.APIURL = server.URL
			configuration.APIKey = "never-include-this-key"
			configuration.TimeoutSeconds = int(time.Second / time.Second)
			configuration.MaxResponseBytes = test.limit
			_, err := (Client{}).Search(context.Background(), configuration, SearchRequest{Query: "go", NumResults: 1})
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), configuration.APIKey) {
				t.Fatalf("error = %v, want %v without key", err, test.want)
			}
		})
	}
}
