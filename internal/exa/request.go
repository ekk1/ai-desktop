package exa

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

const defaultNumResults = 10

type SearchRequest struct {
	Query      string `json:"query"`
	NumResults int    `json:"num_results"`
}

type detectedRequest struct {
	Tool      string            `json:"tool"`
	Arguments detectedArguments `json:"arguments"`
}

type detectedArguments struct {
	Query      string `json:"query"`
	NumResults *int   `json:"num_results,omitempty"`
}

func Detect(content string) (SearchRequest, bool) {
	decoder := json.NewDecoder(strings.NewReader(content))
	decoder.DisallowUnknownFields()
	var detected detectedRequest
	if err := decoder.Decode(&detected); err != nil {
		return SearchRequest{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SearchRequest{}, false
	}
	query := strings.TrimSpace(detected.Arguments.Query)
	if detected.Tool != "exa.search" || query == "" {
		return SearchRequest{}, false
	}
	numResults := defaultNumResults
	if detected.Arguments.NumResults != nil {
		numResults = *detected.Arguments.NumResults
	}
	if numResults < 1 || numResults > 100 {
		return SearchRequest{}, false
	}
	return SearchRequest{Query: query, NumResults: numResults}, true
}
