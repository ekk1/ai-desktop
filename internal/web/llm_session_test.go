package web

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ekk1/ai-desktop/internal/session"
)

func TestLLMSessionAndPanelAPILifecycle(t *testing.T) {
	fixture := newLLMWebFixture(t)
	recorder := fixture.request(t, http.MethodPost, "/api/v1/llm/sessions", []byte(`{"title":"Research","folder":"work"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create session status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var created sessionWorkspaceResponse
	if err := json.NewDecoder(recorder.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Session.Title != "Research" || len(created.Panels) != 1 || len(created.CurrentPath) != 1 {
		t.Fatalf("created session = %#v", created)
	}
	sessionID := created.Session.ID
	rootID := created.Panels[0].ID

	recorder = fixture.request(t, http.MethodGet, "/api/v1/llm/sessions?folder=work&q=search", nil)
	if recorder.Code != http.StatusOK || !containsJSON(recorder.Body.Bytes(), sessionID) {
		t.Fatalf("filtered list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = fixture.request(t, http.MethodPut, "/api/v1/llm/sessions/"+sessionID, []byte(`{"title":"Renamed","folder":"moved","current_panel_id":"`+rootID+`"}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("update session status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = fixture.request(t, http.MethodPost, "/api/v1/llm/sessions/"+sessionID+"/panels", []byte(`{"parent_id":"`+rootID+`","title":"Tool","content":"{\"tool\":\"exa.search\",\"arguments\":{\"query\":\"go\"}}","included":true,"collapsed":false,"knowledge_ids":[],"asset_ids":[]}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create panel status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var child panelResponse
	if err := json.NewDecoder(recorder.Body).Decode(&child); err != nil {
		t.Fatal(err)
	}
	if !child.ExaCandidate {
		t.Fatalf("child is not marked as Exa candidate: %#v", child)
	}

	recorder = fixture.request(t, http.MethodPut, "/api/v1/llm/sessions/"+sessionID+"/panels/"+child.ID, []byte(`{"title":"Changed","content":"changed","included":true,"collapsed":true,"knowledge_ids":[],"asset_ids":[]}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("update panel status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var updated panelResponse
	if err := json.NewDecoder(recorder.Body).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if len(updated.Revisions) != 1 || updated.ExaCandidate {
		t.Fatalf("updated panel = %#v", updated)
	}

	revisionID := updated.Revisions[0].ID
	recorder = fixture.request(t, http.MethodPost, "/api/v1/llm/sessions/"+sessionID+"/panels/"+child.ID+"/restore/"+revisionID, []byte(`{}`))
	if recorder.Code != http.StatusOK {
		t.Fatalf("restore status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = fixture.request(t, http.MethodPost, "/api/v1/llm/sessions/"+sessionID+"/fork", []byte(`{"panel_id":"`+child.ID+`","title":"Fork","folder":"copies"}`))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("fork status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var forked sessionWorkspaceResponse
	if err := json.NewDecoder(recorder.Body).Decode(&forked); err != nil {
		t.Fatal(err)
	}
	if forked.Session.ID == sessionID || len(forked.Panels) != 2 {
		t.Fatalf("forked session = %#v", forked)
	}

	recorder = fixture.request(t, http.MethodGet, "/api/v1/llm/sessions/"+sessionID, nil)
	if recorder.Code != http.StatusOK || !containsJSON(recorder.Body.Bytes(), `"exa_candidate":true`) {
		t.Fatalf("get session status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder = fixture.request(t, http.MethodDelete, "/api/v1/llm/sessions/"+sessionID+"/panels/"+child.ID, nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete panel status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	recorder = fixture.request(t, http.MethodDelete, "/api/v1/llm/sessions/"+sessionID, nil)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("delete session status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestLLMSessionAPIRejectsUnknownFieldsAndMalformedResources(t *testing.T) {
	fixture := newLLMWebFixture(t)
	recorder := fixture.request(t, http.MethodPost, "/api/v1/llm/sessions", []byte(`{"title":"S","unknown":true}`))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d", recorder.Code)
	}
	recorder = fixture.request(t, http.MethodGet, "/api/v1/llm/sessions/not-a-valid-id", nil)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("malformed ID status = %d", recorder.Code)
	}
}

func containsJSON(contents []byte, value string) bool {
	return string(contents) != "" && (json.Valid(contents) && stringContains(string(contents), value))
}

func stringContains(contents, value string) bool {
	for index := 0; index+len(value) <= len(contents); index++ {
		if contents[index:index+len(value)] == value {
			return true
		}
	}
	return false
}

var _ session.Panel
