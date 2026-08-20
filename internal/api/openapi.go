package api

import (
	"net/http"
	"reflect"
	"strings"
	"time"
)

type openAPIRoute struct {
	method   string
	path     string
	action   string
	status   string
	request  string
	response string
	mode     string
	media    string
	class    RateClass
	public   bool
}

var openAPIRoutes = []openAPIRoute{
	publicAPIRoute("get", "/api/v1/health", "health.read"),
	publicAPIRoute("get", "/api/v1/openapi.json", "openapi.read"),
	apiRoute("get", "/api/v1/meta", "meta.read", "200", "", "", "raw", RateRead),
	apiRoute("post", "/api/v1/root/platform-keys", "platform_keys.create", "201", "CreateKeyInput", "IssuedKey", "single", RateNone),
	apiRoute("get", "/api/v1/root/platform-keys", "platform_keys.list", "200", "", "KeyMetadata", "list", RateRead),
	apiRoute("post", "/api/v1/root/platform-keys/{key_id}/rotate", "platform_keys.rotate", "201", "", "IssuedKey", "single", RateNone),
	apiRoute("post", "/api/v1/root/platform-keys/{key_id}/revoke", "platform_keys.revoke", "204", "", "", "raw", RateNone),
	apiRoute("put", "/api/v1/tenants/{external_tenant_id}", "tenants.provision", "200", "", "TenantBinding", "single", RateNone),
	apiRoute("get", "/api/v1/tenants/{external_tenant_id}", "tenants.read", "200", "", "TenantBinding", "single", RateRead),
	apiRoute("post", "/api/v1/tenants/{external_tenant_id}/keys", "tenant_keys.create_initial", "201", "CreateKeyInput", "IssuedKey", "single", RateNone),
	apiRoute("post", "/api/v1/keys", "tenant_keys.create", "201", "DelegatedKeyInput", "IssuedKey", "single", RateNone),
	apiRoute("get", "/api/v1/keys", "tenant_keys.list", "200", "", "KeyMetadata", "list", RateRead),
	apiRoute("post", "/api/v1/keys/{key_id}/rotate", "tenant_keys.rotate", "201", "", "IssuedKey", "single", RateNone),
	apiRoute("post", "/api/v1/keys/{key_id}/revoke", "tenant_keys.revoke", "204", "", "", "raw", RateNone),
	apiRoute("put", "/api/v1/projects/{external_project_id}", "projects.put", "201", "ProjectInput", "Project", "single", RateNone),
	apiRoute("get", "/api/v1/projects", "projects.list", "200", "", "Project", "page", RateRead),
	apiRoute("get", "/api/v1/projects/{project_id}", "projects.read", "200", "", "Project", "single", RateRead),
	apiRoute("patch", "/api/v1/projects/{project_id}", "projects.patch", "200", "ProjectPatch", "Project", "single", RateNone),
	apiRoute("post", "/api/v1/projects/{project_id}/crawls", "crawls.start", "202", "", "APICrawl", "single", RateCrawl),
	apiRoute("get", "/api/v1/projects/{project_id}/crawls", "crawls.list", "200", "", "APICrawl", "page", RateRead),
	apiRoute("get", "/api/v1/projects/{project_id}/crawls/{crawl_id}", "crawls.read", "200", "", "APICrawl", "single", RateRead),
	apiRoute("post", "/api/v1/projects/{project_id}/crawls/{crawl_id}/cancel", "crawls.cancel", "202", "", "APICrawl", "single", RateNone),
	apiRoute("get", "/api/v1/projects/{project_id}/crawls/{crawl_id}/issues", "findings.issues", "200", "", "IssueFinding", "page", RateRead),
	apiRoute("get", "/api/v1/projects/{project_id}/crawls/{crawl_id}/pages", "findings.pages", "200", "", "PageFinding", "page", RateRead),
	apiRoute("get", "/api/v1/projects/{project_id}/crawls/{crawl_id}/links", "findings.links", "200", "", "LinkFinding", "page", RateRead),
	apiRoute("get", "/api/v1/projects/{project_id}/crawls/{crawl_id}/resources", "findings.resources", "200", "", "ResourceFinding", "page", RateRead),
	exportAPIRoute("/api/v1/projects/{project_id}/crawls/{crawl_id}/exports/issues.csv", "exports.issues_csv", "text/csv"),
	exportAPIRoute("/api/v1/projects/{project_id}/crawls/{crawl_id}/exports/pages.csv", "exports.pages_csv", "text/csv"),
	exportAPIRoute("/api/v1/projects/{project_id}/crawls/{crawl_id}/exports/resources.csv", "exports.resources_csv", "text/csv"),
	exportAPIRoute("/api/v1/projects/{project_id}/crawls/{crawl_id}/exports/sitemap.xml", "exports.sitemap", "application/xml"),
	exportAPIRoute("/api/v1/projects/{project_id}/crawls/{crawl_id}/exports/archive.wacz", "exports.archive", "application/wacz"),
}

func publicAPIRoute(method, path, action string) openAPIRoute {
	return openAPIRoute{method: method, path: path, action: action, status: "200", mode: "raw", public: true}
}

func apiRoute(method, path, action, status, request, response, mode string, class RateClass) openAPIRoute {
	return openAPIRoute{method: method, path: path, action: action, status: status, request: request, response: response, mode: mode, class: class}
}

func exportAPIRoute(path, action, media string) openAPIRoute {
	return openAPIRoute{method: "get", path: path, action: action, status: "200", mode: "file", media: media, class: RateExport}
}

func (s *server) openAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, buildOpenAPIDocument(s.deps.Build))
}

func buildOpenAPIDocument(build BuildInfo) map[string]any {
	version := build.ForkVersion
	if version == "" {
		version = "development"
	}
	schemas := map[string]any{}
	for name, value := range map[string]any{
		"CreateKeyInput": CreateKeyInput{}, "DelegatedKeyInput": DelegatedKeyInput{}, "KeyMetadata": KeyMetadata{}, "IssuedKey": IssuedKey{},
		"TenantBinding": TenantBinding{}, "ProjectInput": ProjectInput{}, "ProjectPatch": ProjectPatch{}, "Project": Project{}, "APICrawl": APICrawl{},
		"IssueFinding": IssueFinding{}, "PageFinding": PageFinding{}, "LinkFinding": LinkFinding{}, "ResourceFinding": ResourceFinding{},
	} {
		schemas[name] = schemaFromType(reflect.TypeOf(value))
	}
	paths := map[string]any{}
	for _, route := range openAPIRoutes {
		operations, _ := paths[route.path].(map[string]any)
		if operations == nil {
			operations = map[string]any{}
			paths[route.path] = operations
		}
		responses := map[string]any{route.status: responseSchema(route)}
		if !route.public {
			responses["429"] = errorResponseSchema()
		}
		if route.action == "exports.archive" {
			responses["202"] = map[string]any{"description": "Archive is still being prepared", "headers": map[string]any{"Retry-After": map[string]any{"schema": map[string]any{"type": "integer"}}}}
		}
		operation := map[string]any{
			"operationId": strings.ReplaceAll(route.action, ".", "_"),
			"responses":   responses,
			"parameters":  pathParameters(route.path),
		}
		if !route.public {
			operation["security"] = []any{map[string]any{"bearerAuth": []string{}}}
		}
		if route.request != "" {
			operation["requestBody"] = map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": schemaReference(route.request)}}}
		}
		operations[route.method] = operation
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "SEOnaut Campanix API", "version": version},
		"paths":   paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{"bearerAuth": map[string]any{"type": "http", "scheme": "bearer", "bearerFormat": "SEOnaut API key"}},
			"schemas":         schemas,
		},
	}
}

func responseSchema(route openAPIRoute) map[string]any {
	if route.mode == "file" {
		return map[string]any{"description": "Export stream", "content": map[string]any{route.media: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}}
	}
	if route.response == "" {
		return map[string]any{"description": "Successful response"}
	}
	dataSchema := any(schemaReference(route.response))
	if route.mode == "list" || route.mode == "page" {
		dataSchema = map[string]any{"type": "array", "items": schemaReference(route.response)}
	}
	properties := map[string]any{"data": dataSchema, "request_id": map[string]any{"type": "string"}}
	if route.mode == "page" {
		properties["page"] = map[string]any{"type": "object", "properties": map[string]any{
			"next_cursor": map[string]any{"type": []string{"string", "null"}}, "limit": map[string]any{"type": "integer"},
		}}
	}
	return map[string]any{"description": "Successful response", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object", "properties": properties}}}}
}

func errorResponseSchema() map[string]any {
	return map[string]any{"description": "Rate limit reached", "content": map[string]any{"application/json": map[string]any{"schema": map[string]any{"type": "object"}}}}
}

func schemaReference(name string) map[string]any {
	return map[string]any{"$ref": "#/components/schemas/" + name}
}

func pathParameters(path string) []any {
	parameters := []any{}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			name := strings.TrimSuffix(strings.TrimPrefix(part, "{"), "}")
			parameters = append(parameters, map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string"}})
		}
	}
	return parameters
}

func schemaFromType(value reflect.Type) map[string]any {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value == reflect.TypeOf(time.Time{}) {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	switch value.Kind() {
	case reflect.Struct:
		properties := map[string]any{}
		required := []string{}
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			if field.PkgPath != "" {
				continue
			}
			tag := field.Tag.Get("json")
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "-" {
				continue
			}
			if field.Anonymous && name == "" {
				embedded := schemaFromType(field.Type)
				if embeddedProperties, ok := embedded["properties"].(map[string]any); ok {
					for embeddedName, schema := range embeddedProperties {
						properties[embeddedName] = schema
					}
				}
				continue
			}
			if name == "" {
				name = field.Name
			}
			properties[name] = schemaFromType(field.Type)
			if field.Type.Kind() != reflect.Pointer && !containsString(parts[1:], "omitempty") {
				required = append(required, name)
			}
		}
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFromType(value.Elem())}
	case reflect.Map, reflect.Interface:
		return map[string]any{"type": "object"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	default:
		return map[string]any{"type": "string"}
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
