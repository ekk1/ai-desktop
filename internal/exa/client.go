package exa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ekk1/ai-desktop/internal/provider"
)

const maxErrorExcerptBytes = 4096

var (
	ErrMissingAPIKey   = errors.New("Exa API key is not configured")
	ErrHTTPStatus      = errors.New("Exa returned a non-success HTTP status")
	ErrResponseLimit   = errors.New("Exa response exceeded its size limit")
	ErrInvalidResponse = errors.New("Exa returned invalid JSON")
)

type Client struct{}

type searchBody struct {
	Query      string       `json:"query"`
	NumResults int          `json:"numResults"`
	Contents   bodyContents `json:"contents"`
}

type bodyContents struct {
	Text bool `json:"text"`
}

func (Client) Search(ctx context.Context, configuration provider.ExaConfig, search SearchRequest) (json.RawMessage, error) {
	if configuration.APIKey == "" {
		return nil, ErrMissingAPIKey
	}
	parsedURL, err := url.Parse(configuration.APIURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, fmt.Errorf("invalid Exa API URL")
	}
	search.Query = strings.TrimSpace(search.Query)
	if search.Query == "" || search.NumResults < 1 || search.NumResults > 100 {
		return nil, fmt.Errorf("invalid Exa search request")
	}
	if configuration.TimeoutSeconds < 1 || configuration.MaxResponseBytes < 1 {
		return nil, fmt.Errorf("invalid Exa timeout or response limit")
	}
	body, err := json.Marshal(searchBody{
		Query: search.Query, NumResults: search.NumResults, Contents: bodyContents{Text: true},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Exa request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, configuration.APIURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Exa request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-api-key", configuration.APIKey)
	timeout := time.Duration(configuration.TimeoutSeconds) * time.Second
	transport := &http.Transport{
		Proxy:             http.ProxyFromEnvironment,
		DialContext:       (&net.Dialer{Timeout: min(timeout, 10*time.Second)}).DialContext,
		ForceAttemptHTTP2: true,
	}
	defer transport.CloseIdleConnections()
	response, err := (&http.Client{Transport: transport, Timeout: timeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("execute Exa request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit := int64(maxErrorExcerptBytes)
		if configuration.MaxResponseBytes < limit {
			limit = configuration.MaxResponseBytes
		}
		excerpt, readErr := io.ReadAll(io.LimitReader(response.Body, limit))
		if readErr != nil {
			return nil, fmt.Errorf("read Exa error response: %w", readErr)
		}
		return nil, fmt.Errorf("%w: status %d: %s", ErrHTTPStatus, response.StatusCode, excerpt)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, configuration.MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read Exa response: %w", err)
	}
	if int64(len(contents)) > configuration.MaxResponseBytes {
		return nil, ErrResponseLimit
	}
	if !json.Valid(contents) {
		return nil, ErrInvalidResponse
	}
	return append(json.RawMessage(nil), contents...), nil
}
