package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

var ErrTenantNotFound = errors.New("api tenant not found")

type TenantBinding struct {
	ID           string    `json:"id"`
	ExternalID   string    `json:"external_id"`
	State        string    `json:"state"`
	ServiceEmail string    `json:"service_email"`
	CreatedAt    time.Time `json:"created_at,omitempty"`
}

type DelegatedKeyInput struct {
	Kind      KeyKind    `json:"kind"`
	ProjectID string     `json:"project_id,omitempty"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type TenantService interface {
	ProvisionTenant(context.Context, string) (TenantBinding, error)
	GetTenant(context.Context, string) (TenantBinding, error)
	IssueTenantKey(context.Context, string, CreateKeyInput) (IssuedKey, error)
	IssueDelegatedKey(context.Context, Principal, DelegatedKeyInput) (IssuedKey, error)
	ListTenantKeys(context.Context, Principal) ([]KeyMetadata, error)
	RotateTenantKey(context.Context, Principal, string) (IssuedKey, error)
	RevokeTenantKey(context.Context, Principal, string) error
}

type TenantStore interface {
	ProvisionTenant(context.Context, string) (TenantBinding, error)
	GetTenant(context.Context, string) (TenantBinding, error)
}

type TenantManager struct {
	Store TenantStore
	Keys  KeyManager
}

func (m TenantManager) ProvisionTenant(ctx context.Context, externalID string) (TenantBinding, error) {
	if m.Store == nil || externalID == "" {
		return TenantBinding{}, ErrTenantNotFound
	}
	return m.Store.ProvisionTenant(ctx, externalID)
}

func (m TenantManager) GetTenant(ctx context.Context, externalID string) (TenantBinding, error) {
	if m.Store == nil || externalID == "" {
		return TenantBinding{}, ErrTenantNotFound
	}
	return m.Store.GetTenant(ctx, externalID)
}

func (m TenantManager) IssueTenantKey(ctx context.Context, externalID string, input CreateKeyInput) (IssuedKey, error) {
	binding, err := m.GetTenant(ctx, externalID)
	if err != nil {
		return IssuedKey{}, err
	}
	return m.Keys.CreateTenantKey(ctx, binding.ID, input)
}

func (m TenantManager) IssueDelegatedKey(ctx context.Context, principal Principal, input DelegatedKeyInput) (IssuedKey, error) {
	return m.Keys.CreateDelegatedKey(ctx, principal, input)
}

func (m TenantManager) ListTenantKeys(ctx context.Context, principal Principal) ([]KeyMetadata, error) {
	return m.Keys.ListTenantKeys(ctx, principal)
}

func (m TenantManager) RotateTenantKey(ctx context.Context, principal Principal, publicID string) (IssuedKey, error) {
	return m.Keys.RotateTenantKey(ctx, principal, publicID)
}

func (m TenantManager) RevokeTenantKey(ctx context.Context, principal Principal, publicID string) error {
	return m.Keys.RevokeTenantKey(ctx, principal, publicID)
}

func (s *server) provisionTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r, ScopeTenantsProvision) {
		return
	}
	if s.deps.Tenants == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Tenant provisioning is unavailable")
		return
	}
	binding, err := s.deps.Tenants.ProvisionTenant(r.Context(), r.PathValue("external_tenant_id"))
	if err != nil {
		writeTenantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": binding, "request_id": requestIDFrom(r.Context())})
}

func (s *server) getTenant(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r, ScopeTenantsProvision) {
		return
	}
	if s.deps.Tenants == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Tenant provisioning is unavailable")
		return
	}
	binding, err := s.deps.Tenants.GetTenant(r.Context(), r.PathValue("external_tenant_id"))
	if err != nil {
		writeTenantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": binding, "request_id": requestIDFrom(r.Context())})
}

func (s *server) issueInitialTenantKey(w http.ResponseWriter, r *http.Request) {
	if !s.requirePlatform(w, r, ScopeTenantKeysCreate) {
		return
	}
	var input CreateKeyInput
	if !decodeStrictJSON(w, r, &input) || !validTenantScopes(input.Scopes, KeyTenant) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The tenant key request is invalid")
		return
	}
	if s.deps.Tenants == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Tenant provisioning is unavailable")
		return
	}
	issued, err := s.deps.Tenants.IssueTenantKey(r.Context(), r.PathValue("external_tenant_id"), input)
	if err != nil {
		writeTenantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": issued, "request_id": requestIDFrom(r.Context())})
}

func (s *server) issueDelegatedKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireTenantManager(w, r)
	if !ok {
		return
	}
	var input DelegatedKeyInput
	if !decodeStrictJSON(w, r, &input) || !validTenantScopes(input.Scopes, input.Kind) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The delegated key request is invalid")
		return
	}
	if s.deps.Tenants == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Tenant key management is unavailable")
		return
	}
	issued, err := s.deps.Tenants.IssueDelegatedKey(r.Context(), principal, input)
	if err != nil {
		writeTenantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": issued, "request_id": requestIDFrom(r.Context())})
}

func (s *server) listTenantKeys(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireTenantManager(w, r)
	if !ok {
		return
	}
	keys, err := s.deps.Tenants.ListTenantKeys(r.Context(), principal)
	if err != nil {
		writeTenantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": keys, "request_id": requestIDFrom(r.Context())})
}

func (s *server) rotateTenantKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireTenantManager(w, r)
	if !ok {
		return
	}
	issued, err := s.deps.Tenants.RotateTenantKey(r.Context(), principal, r.PathValue("key_id"))
	if err != nil {
		writeTenantError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": issued, "request_id": requestIDFrom(r.Context())})
}

func (s *server) revokeTenantKey(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.requireTenantManager(w, r)
	if !ok {
		return
	}
	if err := s.deps.Tenants.RevokeTenantKey(r.Context(), principal, r.PathValue("key_id")); err != nil {
		writeTenantError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) requirePlatform(w http.ResponseWriter, r *http.Request, scope string) bool {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return false
	}
	if principal.Kind != KeyPlatform || !principal.Scopes.Has(scope) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return false
	}
	return true
}

func (s *server) requireTenantManager(w http.ResponseWriter, r *http.Request) (Principal, bool) {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return Principal{}, false
	}
	if principal.Kind != KeyTenant || principal.TenantID == "" || !principal.Scopes.Has(ScopeKeysManage) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return Principal{}, false
	}
	return principal, true
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value) == nil
}

func writeTenantError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrTenantNotFound) {
		writeError(w, r, http.StatusNotFound, "tenant_not_found", "Tenant was not found")
		return
	}
	if errors.Is(err, ErrKeyNotFound) {
		writeError(w, r, http.StatusNotFound, "key_not_found", "Key was not found")
		return
	}
	if errors.Is(err, ErrInvalidKeyRequest) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The key request is invalid")
		return
	}
	writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed")
}
