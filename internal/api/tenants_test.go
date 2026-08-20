package api

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type memoryTenants struct {
	bindings map[string]TenantBinding
	keys     map[string][]IssuedKey
	calls    int
}

func (m *memoryTenants) ProvisionTenant(_ context.Context, externalID string) (TenantBinding, error) {
	if binding, ok := m.bindings[externalID]; ok {
		return binding, nil
	}
	m.calls++
	binding := TenantBinding{ID: "tenant-a", ExternalID: externalID, State: "active", ServiceEmail: externalID + "@service.invalid"}
	m.bindings[externalID] = binding
	return binding, nil
}

func (m *memoryTenants) GetTenant(_ context.Context, externalID string) (TenantBinding, error) {
	binding, ok := m.bindings[externalID]
	if !ok {
		return TenantBinding{}, ErrTenantNotFound
	}
	return binding, nil
}

func (m *memoryTenants) IssueTenantKey(_ context.Context, externalID string, input CreateKeyInput) (IssuedKey, error) {
	binding, ok := m.bindings[externalID]
	if !ok {
		return IssuedKey{}, ErrTenantNotFound
	}
	issued := IssuedKey{Key: "snk_prod_tenant01.one-time", KeyMetadata: KeyMetadata{PublicID: "tenant01", Kind: KeyTenant, TenantID: binding.ID, Scopes: input.Scopes}}
	m.keys[binding.ID] = append(m.keys[binding.ID], issued)
	return issued, nil
}

func (m *memoryTenants) IssueDelegatedKey(_ context.Context, principal Principal, input DelegatedKeyInput) (IssuedKey, error) {
	issued := IssuedKey{Key: "snk_prod_read01.one-time", KeyMetadata: KeyMetadata{PublicID: "read01", Kind: input.Kind, TenantID: principal.TenantID, ProjectID: input.ProjectID, Scopes: input.Scopes}}
	m.keys[principal.TenantID] = append(m.keys[principal.TenantID], issued)
	return issued, nil
}

func (m *memoryTenants) ListTenantKeys(_ context.Context, principal Principal) ([]KeyMetadata, error) {
	keys := m.keys[principal.TenantID]
	out := make([]KeyMetadata, 0, len(keys))
	for _, key := range keys {
		out = append(out, key.KeyMetadata)
	}
	return out, nil
}

func (m *memoryTenants) RotateTenantKey(_ context.Context, principal Principal, publicID string) (IssuedKey, error) {
	for _, key := range m.keys[principal.TenantID] {
		if key.PublicID == publicID {
			return IssuedKey{Key: "snk_prod_rotated.one-time", KeyMetadata: KeyMetadata{PublicID: "rotated", Kind: key.Kind, TenantID: principal.TenantID, ProjectID: key.ProjectID, Scopes: key.Scopes, RotatedFrom: publicID}}, nil
		}
	}
	return IssuedKey{}, ErrKeyNotFound
}

func (m *memoryTenants) RevokeTenantKey(_ context.Context, principal Principal, publicID string) error {
	for _, key := range m.keys[principal.TenantID] {
		if key.PublicID == publicID {
			return nil
		}
	}
	return ErrKeyNotFound
}

func TestPlatformProvisioningIsIdempotentAndDoesNotExposeAUsablePassword(t *testing.T) {
	tenants := &memoryTenants{bindings: map[string]TenantBinding{}, keys: map[string][]IssuedKey{}}
	h := NewHandler(Dependencies{Authenticate: platformAuthenticator, Tenants: tenants})

	for range 2 {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/tenants/acme", nil)
		req.Header.Set("Authorization", "Bearer platform")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", res.Code, res.Body.String())
		}
		if bytes.Contains(res.Body.Bytes(), []byte("password")) {
			t.Fatalf("service-user credential leaked: %s", res.Body.String())
		}
	}
	if tenants.calls != 1 {
		t.Fatalf("provision calls=%d, want 1", tenants.calls)
	}
}

func TestPlatformIssuesInitialTenantKeyButCannotListTenantKeys(t *testing.T) {
	tenants := &memoryTenants{bindings: map[string]TenantBinding{"acme": {ID: "tenant-a", ExternalID: "acme", State: "active"}}, keys: map[string][]IssuedKey{}}
	h := NewHandler(Dependencies{Authenticate: platformAuthenticator, Tenants: tenants})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/tenants/acme/keys", bytes.NewBufferString(`{"scopes":["projects:read","keys:manage"]}`))
	req.Header.Set("Authorization", "Bearer platform")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusCreated || !bytes.Contains(res.Body.Bytes(), []byte("one-time")) {
		t.Fatalf("issue status/body=%d %s", res.Code, res.Body.String())
	}

	list := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	list.Header.Set("Authorization", "Bearer platform")
	listRes := httptest.NewRecorder()
	h.ServeHTTP(listRes, list)
	if listRes.Code != http.StatusForbidden {
		t.Fatalf("platform listed tenant data: %d %s", listRes.Code, listRes.Body.String())
	}
}

func TestTenantIssuerCannotCreateAKeyForAnotherTenantOrProject(t *testing.T) {
	tenants := &memoryTenants{bindings: map[string]TenantBinding{}, keys: map[string][]IssuedKey{}}
	h := NewHandler(Dependencies{Authenticate: tenantAuthenticator, Tenants: tenants})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/keys", bytes.NewBufferString(`{"kind":"read_only","tenant_id":"tenant-b","project_id":"project-b","scopes":["projects:read"]}`))
	req.Header.Set("Authorization", "Bearer tenant-a")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("foreign authority fields accepted: %d %s", res.Code, res.Body.String())
	}
}

func TestTenantKeyLifecycleIsBoundToAuthenticatedTenant(t *testing.T) {
	tenants := &memoryTenants{bindings: map[string]TenantBinding{}, keys: map[string][]IssuedKey{"tenant-a": {{KeyMetadata: KeyMetadata{PublicID: "a-key", Kind: KeyReadOnly, TenantID: "tenant-a", Scopes: []string{ScopeProjectsRead}}}}, "tenant-b": {{KeyMetadata: KeyMetadata{PublicID: "b-key", Kind: KeyReadOnly, TenantID: "tenant-b"}}}}}
	h := NewHandler(Dependencies{Authenticate: tenantAuthenticator, Tenants: tenants})

	list := httptest.NewRequest(http.MethodGet, "/api/v1/keys", nil)
	list.Header.Set("Authorization", "Bearer tenant-a")
	listRes := httptest.NewRecorder()
	h.ServeHTTP(listRes, list)
	if listRes.Code != http.StatusOK || bytes.Contains(listRes.Body.Bytes(), []byte("b-key")) || !bytes.Contains(listRes.Body.Bytes(), []byte("a-key")) {
		t.Fatalf("tenant list crossed boundary: %d %s", listRes.Code, listRes.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/v1/keys/b-key/revoke", nil)
	revoke.Header.Set("Authorization", "Bearer tenant-a")
	revokeRes := httptest.NewRecorder()
	h.ServeHTTP(revokeRes, revoke)
	if revokeRes.Code != http.StatusNotFound {
		t.Fatalf("foreign revoke disclosed key: %d %s", revokeRes.Code, revokeRes.Body.String())
	}
}

func platformAuthenticator(context.Context, string) (Principal, error) {
	return Principal{Kind: KeyPlatform, KeyID: "platform", Scopes: NewScopeSet(ScopeTenantsProvision, ScopeTenantKeysCreate)}, nil
}

func tenantAuthenticator(context.Context, string) (Principal, error) {
	return Principal{Kind: KeyTenant, KeyID: "tenant-key", TenantID: "tenant-a", Scopes: NewScopeSet(ScopeKeysManage)}, nil
}
