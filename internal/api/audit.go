package api

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

type AuditRecord struct {
	RequestID   string
	KeyPublicID string
	TenantID    string
	ProjectID   string
	CrawlID     string
	Action      string
	Outcome     string
	HTTPStatus  int
	SourceIP    string
	Metadata    map[string]string
	CreatedAt   time.Time
}

func (r AuditRecord) MetadataJSON() string {
	encoded, _ := json.Marshal(r.Metadata)
	return string(encoded)
}

type AuditSink interface {
	RecordAudit(context.Context, AuditRecord) error
}

type authenticationResult struct {
	principal Principal
	err       error
}

type authenticationResultKey struct{}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *statusRecorder) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *server) operationalMiddleware(action string, class RateClass, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result := authenticationResult{err: ErrUnauthenticated}
		if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" && s.deps.Authenticate != nil {
			result.principal, result.err = s.deps.Authenticate(r.Context(), authorization)
		}
		r = r.WithContext(context.WithValue(r.Context(), authenticationResultKey{}, result))
		recorder := &statusRecorder{ResponseWriter: w}
		defer s.recordAudit(r, action, result.principal, recorder)
		if result.err == nil && class == RateExport && s.deps.ExportSlots != nil {
			release, acquired := s.deps.ExportSlots.Acquire(result.principal.TenantID)
			if !acquired {
				recorder.Header().Set("Retry-After", "2")
				writeError(recorder, r, http.StatusTooManyRequests, "rate_limited", "The tenant export concurrency limit was reached")
				return
			}
			defer release()
		}

		if result.err == nil && s.deps.RateLimiter != nil && class != RateNone {
			key := result.principal.KeyID
			if class == RateCrawl || class == RateExport {
				key = result.principal.TenantID
			}
			decision := s.deps.RateLimiter.Allow(key, class)
			setRateHeaders(recorder.Header(), decision, time.Now().UTC())
			if !decision.Allowed {
				writeError(recorder, r, http.StatusTooManyRequests, "rate_limited", "The request rate limit was reached")
				return
			}
		}
		next.ServeHTTP(recorder, r)
	})
}

func (s *server) recordAudit(r *http.Request, action string, principal Principal, recorder *statusRecorder) {
	if s.deps.Audit == nil {
		return
	}
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	record := AuditRecord{
		RequestID: requestIDFrom(r.Context()), KeyPublicID: principal.KeyID, TenantID: principal.TenantID,
		ProjectID: auditOpaqueID(r.PathValue("project_id")), CrawlID: auditOpaqueID(r.PathValue("crawl_id")), Action: action,
		Outcome: auditOutcome(status), HTTPStatus: status, SourceIP: sourceIP(r.RemoteAddr),
		Metadata: map[string]string{"method": r.Method, "key_kind": string(principal.Kind)}, CreatedAt: time.Now().UTC(),
	}
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 2*time.Second)
	defer cancel()
	if err := s.deps.Audit.RecordAudit(auditContext, record); err != nil {
		log.Printf("record API audit request_id=%s action=%s: %v", record.RequestID, action, err)
	}
}

func auditOpaqueID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) != 36 {
		return ""
	}
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != strings.ToLower(value) {
		return ""
	}
	return parsed.String()
}

func auditOutcome(status int) string {
	switch {
	case status >= 200 && status < 400:
		return "success"
	case status == http.StatusUnauthorized:
		return "unauthenticated"
	case status == http.StatusForbidden:
		return "forbidden"
	case status == http.StatusNotFound:
		return "not_found"
	case status == http.StatusConflict:
		return "conflict"
	case status == http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "error"
	}
}

func sourceIP(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	if len(remote) > 64 {
		return remote[:64]
	}
	return remote
}
