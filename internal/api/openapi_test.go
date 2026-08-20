package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAPIContractUsesHandlerTypesAndContainsEveryRouteFamily(t *testing.T) {
	res := httptest.NewRecorder()
	NewHandler(Dependencies{}).ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
	}
	var document map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("openapi=%v", document["openapi"])
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{
		"/api/v1/health", "/api/v1/meta", "/api/v1/root/platform-keys", "/api/v1/tenants/{external_tenant_id}",
		"/api/v1/projects/{project_id}/crawls", "/api/v1/projects/{project_id}/crawls/{crawl_id}/issues",
		"/api/v1/projects/{project_id}/crawls/{crawl_id}/exports/archive.wacz",
	} {
		if _, ok := paths[path]; !ok {
			t.Fatalf("missing OpenAPI path %s", path)
		}
	}
	components := document["components"].(map[string]any)
	schemas := components["schemas"].(map[string]any)
	projectInput := schemas["ProjectInput"].(map[string]any)
	if _, ok := projectInput["properties"].(map[string]any)["url"]; !ok {
		t.Fatalf("ProjectInput was not generated from its JSON fields: %#v", projectInput)
	}
	crawl := schemas["APICrawl"].(map[string]any)
	if _, ok := crawl["properties"].(map[string]any)["state"]; !ok {
		t.Fatalf("APICrawl was not generated from its JSON fields: %#v", crawl)
	}
	serialized := strings.ToLower(res.Body.String())
	for _, forbidden := range []string{"root_hash", "secret_hash", "authorization"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("OpenAPI leaked %q", forbidden)
		}
	}
}
