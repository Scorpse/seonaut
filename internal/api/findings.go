package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

const (
	defaultPageLimit = 100
	maximumPageLimit = 500
)

var ErrCursorInvalid = errors.New("cursor invalid")

type PageRequest struct {
	AfterID int64
	Limit   int
}

type PageResult[T any] struct {
	Items       []T
	NextAfterID int64
}

type IssueFinding struct {
	Code     string         `json:"code"`
	Severity string         `json:"severity"`
	PageURL  string         `json:"page_url"`
	Evidence map[string]any `json:"evidence"`
	Count    int            `json:"count"`
}

type PageFinding struct {
	URL             string `json:"url"`
	RedirectURL     string `json:"redirect_url,omitempty"`
	StatusCode      int    `json:"status_code"`
	ContentType     string `json:"content_type,omitempty"`
	Language        string `json:"language,omitempty"`
	Title           string `json:"title,omitempty"`
	Description     string `json:"description,omitempty"`
	Robots          string `json:"robots,omitempty"`
	Canonical       string `json:"canonical,omitempty"`
	Heading1        string `json:"heading_1,omitempty"`
	Heading2        string `json:"heading_2,omitempty"`
	Words           int    `json:"words"`
	SizeBytes       int64  `json:"size_bytes"`
	Depth           int    `json:"depth"`
	TTFBMillis      int    `json:"ttfb_ms"`
	Crawled         bool   `json:"crawled"`
	InSitemap       bool   `json:"in_sitemap"`
	NoIndex         bool   `json:"noindex"`
	NoFollow        bool   `json:"nofollow"`
	SitemapEligible bool   `json:"-"`
}

type LinkFinding struct {
	Kind           string `json:"kind"`
	OriginURL      string `json:"origin_url"`
	DestinationURL string `json:"destination_url"`
	Text           string `json:"text,omitempty"`
	Rel            string `json:"rel,omitempty"`
	NoFollow       bool   `json:"nofollow"`
}

type ResourceFinding struct {
	Type      string `json:"type"`
	OriginURL string `json:"origin_url"`
	URL       string `json:"url"`
	Alt       string `json:"alt,omitempty"`
	Poster    string `json:"poster,omitempty"`
}

type FindingService interface {
	ListIssues(context.Context, Principal, string, string, PageRequest) (PageResult[IssueFinding], error)
	ListPages(context.Context, Principal, string, string, PageRequest) (PageResult[PageFinding], error)
	ListLinks(context.Context, Principal, string, string, PageRequest) (PageResult[LinkFinding], error)
	ListResources(context.Context, Principal, string, string, string, PageRequest) (PageResult[ResourceFinding], error)
}

type cursorPayload struct {
	Version   int    `json:"v"`
	Route     string `json:"r"`
	ProjectID string `json:"p"`
	CrawlID   string `json:"c"`
	Filter    string `json:"f,omitempty"`
	AfterID   int64  `json:"a"`
}

func encodeCursor(secret []byte, payload cursorPayload) (string, error) {
	payload.Version = 1
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func decodeCursor(secret []byte, token string, expected cursorPayload) (cursorPayload, error) {
	parts := strings.Split(token, ".")
	if len(secret) < 16 || len(parts) != 2 {
		return cursorPayload{}, ErrCursorInvalid
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return cursorPayload{}, ErrCursorInvalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return cursorPayload{}, ErrCursorInvalid
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return cursorPayload{}, ErrCursorInvalid
	}
	var payload cursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.Version != 1 || payload.AfterID <= 0 || payload.Route != expected.Route || payload.ProjectID != expected.ProjectID || payload.CrawlID != expected.CrawlID || payload.Filter != expected.Filter {
		return cursorPayload{}, ErrCursorInvalid
	}
	return payload, nil
}

func (s *server) listIssues(w http.ResponseWriter, r *http.Request) {
	s.serveFindingPage(w, r, "issues", "", func(ctx context.Context, principal Principal, page PageRequest) (any, int64, error) {
		result, err := s.deps.Findings.ListIssues(ctx, principal, r.PathValue("project_id"), r.PathValue("crawl_id"), page)
		return result.Items, result.NextAfterID, err
	})
}

func (s *server) listPages(w http.ResponseWriter, r *http.Request) {
	s.serveFindingPage(w, r, "pages", "", func(ctx context.Context, principal Principal, page PageRequest) (any, int64, error) {
		result, err := s.deps.Findings.ListPages(ctx, principal, r.PathValue("project_id"), r.PathValue("crawl_id"), page)
		return result.Items, result.NextAfterID, err
	})
}

func (s *server) listLinks(w http.ResponseWriter, r *http.Request) {
	s.serveFindingPage(w, r, "links", "", func(ctx context.Context, principal Principal, page PageRequest) (any, int64, error) {
		result, err := s.deps.Findings.ListLinks(ctx, principal, r.PathValue("project_id"), r.PathValue("crawl_id"), page)
		return result.Items, result.NextAfterID, err
	})
}

func (s *server) listResources(w http.ResponseWriter, r *http.Request) {
	resourceType := strings.TrimSpace(r.URL.Query().Get("type"))
	if !validResourceType(resourceType) {
		writeError(w, r, http.StatusBadRequest, "resource_type_invalid", "Resource type is invalid")
		return
	}
	s.serveFindingPage(w, r, "resources", resourceType, func(ctx context.Context, principal Principal, page PageRequest) (any, int64, error) {
		result, err := s.deps.Findings.ListResources(ctx, principal, r.PathValue("project_id"), r.PathValue("crawl_id"), resourceType, page)
		return result.Items, result.NextAfterID, err
	})
}

func validResourceType(value string) bool {
	switch value {
	case "", "image", "script", "style", "iframe", "audio", "video":
		return true
	default:
		return false
	}
}

func (s *server) serveFindingPage(w http.ResponseWriter, r *http.Request, route, filter string, fetch func(context.Context, Principal, PageRequest) (any, int64, error)) {
	principal, ok := s.requireFindingScope(w, r)
	if !ok {
		return
	}
	projectID, crawlID := r.PathValue("project_id"), r.PathValue("crawl_id")
	if principal.ProjectID != "" && principal.ProjectID != projectID {
		writeCrawlError(w, r, ErrCrawlNotFound)
		return
	}
	page, err := parsePageRequest(r, s.deps.CursorSecret, cursorPayload{Route: route, ProjectID: projectID, CrawlID: crawlID, Filter: filter})
	if err != nil {
		if errors.Is(err, ErrCursorInvalid) {
			writeError(w, r, http.StatusBadRequest, "cursor_invalid", "Cursor is invalid")
		} else {
			writeError(w, r, http.StatusBadRequest, "limit_invalid", "Limit must be between 1 and 500")
		}
		return
	}
	items, nextAfterID, err := fetch(r.Context(), principal, page)
	if err != nil {
		writeCrawlError(w, r, err)
		return
	}
	var nextCursor *string
	if nextAfterID > 0 {
		token, err := encodeCursor(s.deps.CursorSecret, cursorPayload{Route: route, ProjectID: projectID, CrawlID: crawlID, Filter: filter, AfterID: nextAfterID})
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed")
			return
		}
		nextCursor = &token
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": items, "page": map[string]any{"next_cursor": nextCursor, "limit": page.Limit}, "request_id": requestIDFrom(r.Context())})
}

func parsePageRequest(r *http.Request, secret []byte, expected cursorPayload) (PageRequest, error) {
	limit := defaultPageLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maximumPageLimit {
			return PageRequest{}, errors.New("invalid limit")
		}
		limit = parsed
	}
	page := PageRequest{Limit: limit}
	if token := strings.TrimSpace(r.URL.Query().Get("cursor")); token != "" {
		payload, err := decodeCursor(secret, token, expected)
		if err != nil {
			return PageRequest{}, err
		}
		page.AfterID = payload.AfterID
	}
	return page, nil
}

func (s *server) requireFindingScope(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return Principal{}, false
	}
	if (principal.Kind != KeyTenant && principal.Kind != KeyReadOnly) || principal.TenantID == "" || !principal.Scopes.Has(ScopeFindingsRead) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return Principal{}, false
	}
	if s.deps.Findings == nil || len(s.deps.CursorSecret) < 16 {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Findings are unavailable")
		return Principal{}, false
	}
	return principal, true
}
