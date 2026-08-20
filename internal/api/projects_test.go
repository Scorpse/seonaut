package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type memoryProjects struct {
	projects map[string]Project
	replays  map[string]Project
	hashes   map[string]string
}

func (m *memoryProjects) PutProject(_ context.Context, principal Principal, externalID, idempotencyKey string, input ProjectInput) (Project, bool, error) {
	if principal.TenantID == "" || idempotencyKey == "" {
		return Project{}, false, ErrIdempotencyKeyRequired
	}
	hash, err := HashProjectPut(externalID, input)
	if err != nil {
		return Project{}, false, err
	}
	replayKey := principal.KeyID + ":put_project:" + idempotencyKey
	if previousHash, ok := m.hashes[replayKey]; ok {
		if previousHash != hash {
			return Project{}, false, ErrIdempotencyConflict
		}
		return m.replays[replayKey], true, nil
	}
	key := principal.TenantID + ":" + externalID
	project, exists := m.projects[key]
	if !exists {
		project = Project{ID: "project-" + externalID, ExternalID: externalID, TenantID: principal.TenantID, CreatedAt: time.Unix(1, 0).UTC()}
	}
	project.ProjectInput = input
	project.UpdatedAt = time.Unix(2, 0).UTC()
	m.projects[key] = project
	m.hashes[replayKey] = hash
	m.replays[replayKey] = project
	return project, false, nil
}

func (m *memoryProjects) ListProjects(_ context.Context, principal Principal) ([]Project, error) {
	projects := []Project{}
	for _, project := range m.projects {
		if project.TenantID == principal.TenantID && (principal.ProjectID == "" || project.ID == principal.ProjectID) {
			projects = append(projects, project)
		}
	}
	return projects, nil
}

func (m *memoryProjects) GetProject(_ context.Context, principal Principal, projectID string) (Project, error) {
	for _, project := range m.projects {
		if project.ID == projectID && project.TenantID == principal.TenantID && (principal.ProjectID == "" || principal.ProjectID == project.ID) {
			return project, nil
		}
	}
	return Project{}, ErrProjectNotFound
}

func (m *memoryProjects) PatchProject(_ context.Context, principal Principal, projectID string, input ProjectPatch) (Project, error) {
	project, err := m.GetProject(context.Background(), principal, projectID)
	if err != nil {
		return Project{}, err
	}
	if input.UserAgent != nil {
		project.UserAgent = *input.UserAgent
	}
	m.projects[project.TenantID+":"+project.ExternalID] = project
	return project, nil
}

func TestProjectRoutesEnforceTenantAndProjectBindings(t *testing.T) {
	projects := &memoryProjects{
		projects: map[string]Project{
			"tenant-a:a": {ID: "project-a", ExternalID: "a", TenantID: "tenant-a", ProjectInput: ProjectInput{URL: "https://a.example", UserAgent: "SEOnaut"}},
			"tenant-b:b": {ID: "project-b", ExternalID: "b", TenantID: "tenant-b", ProjectInput: ProjectInput{URL: "https://b.example", UserAgent: "SEOnaut"}},
		},
		replays: map[string]Project{},
		hashes:  map[string]string{},
	}
	h := NewHandler(Dependencies{Authenticate: projectAuthenticator, Projects: projects})

	tests := []struct {
		name       string
		token      string
		path       string
		status     int
		contains   string
		notContain string
	}{
		{name: "tenant list", token: "tenant-a", path: "/api/v1/projects", status: http.StatusOK, contains: "project-a", notContain: "project-b"},
		{name: "foreign direct id", token: "tenant-a", path: "/api/v1/projects/project-b", status: http.StatusNotFound, contains: "project_not_found"},
		{name: "bound read list", token: "read-a", path: "/api/v1/projects", status: http.StatusOK, contains: "project-a", notContain: "project-b"},
		{name: "bound read foreign id", token: "read-a", path: "/api/v1/projects/project-b", status: http.StatusNotFound, contains: "project_not_found"},
		{name: "platform denied data", token: "platform", path: "/api/v1/projects", status: http.StatusForbidden, contains: "scope_forbidden"},
		{name: "root denied data", token: "root", path: "/api/v1/projects", status: http.StatusForbidden, contains: "scope_forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tt.status || !strings.Contains(res.Body.String(), tt.contains) || tt.notContain != "" && strings.Contains(res.Body.String(), tt.notContain) {
				t.Fatalf("status/body=%d %s", res.Code, res.Body.String())
			}
		})
	}
}

func TestPutProjectRequiresIdempotencyAndReplaysOnlySamePayload(t *testing.T) {
	projects := &memoryProjects{projects: map[string]Project{}, replays: map[string]Project{}, hashes: map[string]string{}}
	h := NewHandler(Dependencies{Authenticate: projectAuthenticator, Projects: projects})
	body := `{"url":"https://example.com","user_agent":"SEOnaut"}`

	request := func(externalID, idempotencyKey, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/"+externalID, bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer tenant-a")
		if idempotencyKey != "" {
			req.Header.Set("Idempotency-Key", idempotencyKey)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}

	if res := request("brand-site", "", body); res.Code != http.StatusBadRequest || !strings.Contains(res.Body.String(), "idempotency_key_required") {
		t.Fatalf("missing key status/body=%d %s", res.Code, res.Body.String())
	}
	first := request("brand-site", "one", body)
	if first.Code != http.StatusCreated || !strings.Contains(first.Body.String(), "project-brand-site") {
		t.Fatalf("first status/body=%d %s", first.Code, first.Body.String())
	}
	replay := request("brand-site", "one", `{ "user_agent": "SEOnaut", "url": "https://example.com" }`)
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replay status/body=%d %s", replay.Code, replay.Body.String())
	}
	conflict := request("brand-site", "one", `{"url":"https://different.example","user_agent":"SEOnaut"}`)
	if conflict.Code != http.StatusConflict || !strings.Contains(conflict.Body.String(), "idempotency_conflict") {
		t.Fatalf("conflict status/body=%d %s", conflict.Code, conflict.Body.String())
	}
	otherResource := request("other-site", "one", body)
	if otherResource.Code != http.StatusConflict || !strings.Contains(otherResource.Body.String(), "idempotency_conflict") {
		t.Fatalf("cross-resource reuse status/body=%d %s", otherResource.Code, otherResource.Body.String())
	}
}

func TestProjectWritesRejectUnknownFieldsForbiddenSchemesAndReadOnlyKeys(t *testing.T) {
	projects := &memoryProjects{projects: map[string]Project{}, replays: map[string]Project{}, hashes: map[string]string{}}
	h := NewHandler(Dependencies{Authenticate: projectAuthenticator, Projects: projects})
	tests := []struct {
		name    string
		token   string
		payload string
		status  int
	}{
		{name: "unknown field", token: "tenant-a", payload: `{"url":"https://example.com","user_agent":"SEOnaut","user_id":42}`, status: http.StatusBadRequest},
		{name: "ftp scheme", token: "tenant-a", payload: `{"url":"ftp://example.com","user_agent":"SEOnaut"}`, status: http.StatusBadRequest},
		{name: "javascript scheme", token: "tenant-a", payload: `{"url":"javascript:alert(1)","user_agent":"SEOnaut"}`, status: http.StatusBadRequest},
		{name: "read only write", token: "read-a", payload: `{"url":"https://example.com","user_agent":"SEOnaut"}`, status: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/v1/projects/new", bytes.NewBufferString(tt.payload))
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.Header.Set("Idempotency-Key", "one")
			res := httptest.NewRecorder()
			h.ServeHTTP(res, req)
			if res.Code != tt.status {
				t.Fatalf("status/body=%d %s", res.Code, res.Body.String())
			}
		})
	}
}

func TestPatchProjectUsesAuthenticatedOwnerAndRejectsUnknownFields(t *testing.T) {
	projects := &memoryProjects{
		projects: map[string]Project{
			"tenant-a:a": {ID: "project-a", ExternalID: "a", TenantID: "tenant-a", ProjectInput: ProjectInput{URL: "https://a.example", UserAgent: "old"}},
			"tenant-b:b": {ID: "project-b", ExternalID: "b", TenantID: "tenant-b", ProjectInput: ProjectInput{URL: "https://b.example", UserAgent: "old"}},
		},
		replays: map[string]Project{},
		hashes:  map[string]string{},
	}
	h := NewHandler(Dependencies{Authenticate: projectAuthenticator, Projects: projects})

	patch := func(path, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPatch, path, bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer tenant-a")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	if res := patch("/api/v1/projects/project-a", `{"user_agent":"new"}`); res.Code != http.StatusOK || !strings.Contains(res.Body.String(), `"user_agent":"new"`) {
		t.Fatalf("patch status/body=%d %s", res.Code, res.Body.String())
	}
	if res := patch("/api/v1/projects/project-b", `{"user_agent":"new"}`); res.Code != http.StatusNotFound {
		t.Fatalf("foreign patch status/body=%d %s", res.Code, res.Body.String())
	}
	if res := patch("/api/v1/projects/project-a", `{"owner_id":2}`); res.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status/body=%d %s", res.Code, res.Body.String())
	}
}

func projectAuthenticator(_ context.Context, authorization string) (Principal, error) {
	switch authorization {
	case "Bearer tenant-a":
		return Principal{Kind: KeyTenant, KeyID: "key-a", TenantID: "tenant-a", Scopes: NewScopeSet(ScopeProjectsRead, ScopeProjectsWrite)}, nil
	case "Bearer read-a":
		return Principal{Kind: KeyReadOnly, KeyID: "read-a", TenantID: "tenant-a", ProjectID: "project-a", Scopes: NewScopeSet(ScopeProjectsRead)}, nil
	case "Bearer platform":
		return Principal{Kind: KeyPlatform, KeyID: "platform", Scopes: NewScopeSet(ScopeProjectsRead, ScopeProjectsWrite)}, nil
	case "Bearer root":
		return Principal{Kind: KeyRoot, KeyID: "root", Scopes: NewScopeSet(ScopeProjectsRead, ScopeProjectsWrite)}, nil
	default:
		return Principal{}, errors.New("unauthenticated")
	}
}
