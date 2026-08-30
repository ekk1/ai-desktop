package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ekk1/ai-desktop/internal/config"
	"github.com/ekk1/ai-desktop/internal/exa"
	"github.com/ekk1/ai-desktop/internal/session"
)

var ErrNotExaCandidate = errors.New("panel is not an exact Exa search request")

type ExaService struct {
	config   *config.Repository
	sessions *session.Service
	client   exa.Client
}

func NewExaService(configRepository *config.Repository, sessions *session.Service, client exa.Client) *ExaService {
	return &ExaService{config: configRepository, sessions: sessions, client: client}
}

func (service *ExaService) Execute(ctx context.Context, sessionID, panelID string) (session.Panel, error) {
	workspace, exists := service.sessions.Get(sessionID)
	if !exists {
		return session.Panel{}, session.ErrSessionNotFound
	}
	var source *session.Panel
	for index := range workspace.Panels {
		if workspace.Panels[index].ID == panelID {
			copy := workspace.Panels[index]
			source = &copy
			break
		}
	}
	if source == nil {
		return session.Panel{}, session.ErrPanelNotFound
	}
	search, candidate := exa.Detect(source.Content)
	if !candidate {
		return session.Panel{}, ErrNotExaCandidate
	}
	response, err := service.client.Search(ctx, service.config.Snapshot().LLM.Exa, search)
	if err != nil {
		return session.Panel{}, err
	}
	var formatted bytes.Buffer
	if err := jsonIndent(&formatted, response); err != nil {
		return session.Panel{}, fmt.Errorf("format Exa response: %w", err)
	}
	return service.sessions.CreatePanel(sessionID, session.CreatePanelInput{
		ParentID: panelID, Title: "Exa: " + search.Query, Content: formatted.String(), Included: true,
		Result: &session.ResultMetadata{
			Source: "exa", RequestSummary: fmt.Sprintf("query=%q; num_results=%d", search.Query, search.NumResults),
		},
	})
}

func jsonIndent(destination *bytes.Buffer, source []byte) error {
	return json.Indent(destination, source, "", "  ")
}
