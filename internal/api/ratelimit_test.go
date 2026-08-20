package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingAuditSink struct {
	mu      sync.Mutex
	records []AuditRecord
}

func (s *recordingAuditSink) RecordAudit(_ context.Context, record AuditRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

func TestReadRateLimitReturnsStandardHeadersAndAuditsRedactedMetadata(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	limiter := NewFixedWindowLimiter(time.Minute, map[RateClass]int{RateRead: 1}, func() time.Time { return now })
	audit := &recordingAuditSink{}
	handler := NewHandler(Dependencies{
		Authenticate: func(context.Context, string) (Principal, error) {
			return Principal{Kind: KeyTenant, KeyID: "key-a", TenantID: "tenant-a", Scopes: NewScopeSet(ScopeMetaRead)}, nil
		},
		RateLimiter: limiter,
		Audit:       audit,
	})

	request := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/meta?secret=do-not-log", strings.NewReader("payload-secret"))
		req.Header.Set("Authorization", "Bearer snk_test_public.super-secret")
		req.Header.Set("Idempotency-Key", "idempotency-secret")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	first := request()
	if first.Code != http.StatusOK || first.Header().Get("RateLimit-Limit") != "1" || first.Header().Get("RateLimit-Remaining") != "0" || first.Header().Get("RateLimit-Reset") == "" {
		t.Fatalf("first status/headers = %d %#v", first.Code, first.Header())
	}
	second := request()
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second status/headers = %d %#v", second.Code, second.Header())
	}
	if len(audit.records) != 2 || audit.records[0].Outcome != "success" || audit.records[1].Outcome != "rate_limited" {
		t.Fatalf("audit records = %#v", audit.records)
	}
	for _, record := range audit.records {
		serialized := record.MetadataJSON()
		for _, secret := range []string{"super-secret", "payload-secret", "idempotency-secret", "secret=do-not-log"} {
			if strings.Contains(serialized, secret) {
				t.Fatalf("audit metadata leaked %q: %s", secret, serialized)
			}
		}
		if record.KeyPublicID != "key-a" || record.TenantID != "tenant-a" || record.Action != "meta.read" {
			t.Fatalf("audit identity/action = %#v", record)
		}
	}
}

func TestAuditSanitizesRouteIdentifiers(t *testing.T) {
	validProjectID := "00000000-0000-4000-8000-000000000001"
	projects := &memoryProjects{
		projects: map[string]Project{
			"tenant-a:valid": {ID: validProjectID, ExternalID: "valid", TenantID: "tenant-a"},
		},
		replays: map[string]Project{},
		hashes:  map[string]string{},
	}
	audit := &recordingAuditSink{}
	handler := NewHandler(Dependencies{Authenticate: projectAuthenticator, Projects: projects, Audit: audit})

	for _, projectID := range []string{validProjectID, strings.Repeat("x", 200), "snk_test_public.secret-token", "0123456789abcdef0123456789abcdef"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+projectID, nil)
		req.Header.Set("Authorization", "Bearer tenant-a")
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
	}

	if len(audit.records) != 4 {
		t.Fatalf("audit records = %#v", audit.records)
	}
	if audit.records[0].ProjectID != validProjectID {
		t.Fatalf("valid project id = %q", audit.records[0].ProjectID)
	}
	for index, record := range audit.records[1:] {
		if record.ProjectID != "" {
			t.Fatalf("unsafe project id %d persisted as %q", index, record.ProjectID)
		}
	}
}

func TestConcurrencyBudgetEnforcesPerTenantAndGlobalLimits(t *testing.T) {
	budget := NewConcurrencyBudget(1, 2)
	releaseA, ok := budget.Acquire("tenant-a")
	if !ok {
		t.Fatal("first tenant-a slot rejected")
	}
	if _, ok := budget.Acquire("tenant-a"); ok {
		t.Fatal("second tenant-a slot allowed")
	}
	releaseB, ok := budget.Acquire("tenant-b")
	if !ok {
		t.Fatal("tenant-b slot rejected")
	}
	if _, ok := budget.Acquire("tenant-c"); ok {
		t.Fatal("global third slot allowed")
	}
	releaseA()
	if releaseC, ok := budget.Acquire("tenant-c"); !ok {
		t.Fatal("slot was not released")
	} else {
		releaseC()
	}
	releaseB()
}
