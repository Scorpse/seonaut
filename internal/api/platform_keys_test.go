package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type memoryPlatformKeys struct {
	keys []KeyMetadata
}

func (m *memoryPlatformKeys) CreatePlatformKey(_ context.Context, input CreateKeyInput) (IssuedKey, error) {
	key := IssuedKey{
		Key:         "snk_prod_platform01.one-time-secret",
		KeyMetadata: KeyMetadata{PublicID: "platform01", Kind: KeyPlatform, Scopes: input.Scopes, CreatedAt: time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC), ExpiresAt: input.ExpiresAt},
	}
	m.keys = append(m.keys, key.KeyMetadata)
	return key, nil
}

func (m *memoryPlatformKeys) ListPlatformKeys(context.Context) ([]KeyMetadata, error) {
	return append([]KeyMetadata(nil), m.keys...), nil
}

func (m *memoryPlatformKeys) RotatePlatformKey(_ context.Context, publicID string) (IssuedKey, error) {
	return IssuedKey{Key: "snk_prod_platform02.rotated-secret", KeyMetadata: KeyMetadata{PublicID: "platform02", Kind: KeyPlatform, RotatedFrom: publicID}}, nil
}

func (m *memoryPlatformKeys) RevokePlatformKey(_ context.Context, publicID string) error {
	for i := range m.keys {
		if m.keys[i].PublicID == publicID {
			now := time.Date(2026, 8, 20, 12, 5, 0, 0, time.UTC)
			m.keys[i].RevokedAt = &now
		}
	}
	return nil
}

func TestPlatformKeyRoutesRequireRootKindNotJustRootScopeNames(t *testing.T) {
	keys := &memoryPlatformKeys{}
	h := NewHandler(Dependencies{
		Authenticate: func(context.Context, string) (Principal, error) {
			return Principal{Kind: KeyTenant, Scopes: NewScopeSet(ScopePlatformKeysCreate)}, nil
		},
		PlatformKeys: keys,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/root/platform-keys", bytes.NewBufferString(`{"scopes":["tenants:provision"]}`))
	req.Header.Set("Authorization", "Bearer tenant-key")
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", res.Code, http.StatusForbidden, res.Body.String())
	}
	assertErrorCode(t, res, "scope_forbidden")
}

func TestRootCreatesPlatformKeyAndSecretAppearsOnlyInCreateResponse(t *testing.T) {
	keys := &memoryPlatformKeys{}
	h := NewHandler(Dependencies{
		Authenticate: rootAuthenticator,
		PlatformKeys: keys,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/root/platform-keys", bytes.NewBufferString(`{"scopes":["tenants:provision","tenant_keys:create"]}`))
	req.Header.Set("Authorization", "Bearer root-key")
	res := httptest.NewRecorder()

	h.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d; body=%s", res.Code, res.Body.String())
	}
	var created struct {
		Data struct {
			Key      string   `json:"key"`
			PublicID string   `json:"public_id"`
			Scopes   []string `json:"scopes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Key != "snk_prod_platform01.one-time-secret" || created.Data.PublicID != "platform01" || len(created.Data.Scopes) != 2 {
		t.Fatalf("created = %#v", created.Data)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/root/platform-keys", nil)
	listReq.Header.Set("Authorization", "Bearer root-key")
	listRes := httptest.NewRecorder()
	h.ServeHTTP(listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("list status = %d; body=%s", listRes.Code, listRes.Body.String())
	}
	if bytes.Contains(listRes.Body.Bytes(), []byte("one-time-secret")) || bytes.Contains(listRes.Body.Bytes(), []byte(`"key"`)) {
		t.Fatalf("list leaked secret material: %s", listRes.Body.String())
	}
}

func TestRootRotatesAndRevokesPlatformKeys(t *testing.T) {
	keys := &memoryPlatformKeys{keys: []KeyMetadata{{PublicID: "platform01", Kind: KeyPlatform}}}
	h := NewHandler(Dependencies{Authenticate: rootAuthenticator, PlatformKeys: keys})

	rotate := httptest.NewRequest(http.MethodPost, "/api/v1/root/platform-keys/platform01/rotate", nil)
	rotate.Header.Set("Authorization", "Bearer root-key")
	rotateRes := httptest.NewRecorder()
	h.ServeHTTP(rotateRes, rotate)
	if rotateRes.Code != http.StatusCreated || !bytes.Contains(rotateRes.Body.Bytes(), []byte("rotated-secret")) {
		t.Fatalf("rotate status/body = %d %s", rotateRes.Code, rotateRes.Body.String())
	}

	revoke := httptest.NewRequest(http.MethodPost, "/api/v1/root/platform-keys/platform01/revoke", nil)
	revoke.Header.Set("Authorization", "Bearer root-key")
	revokeRes := httptest.NewRecorder()
	h.ServeHTTP(revokeRes, revoke)
	if revokeRes.Code != http.StatusNoContent {
		t.Fatalf("revoke status = %d; body=%s", revokeRes.Code, revokeRes.Body.String())
	}
	if keys.keys[0].RevokedAt == nil {
		t.Fatal("key was not revoked")
	}
}

func rootAuthenticator(context.Context, string) (Principal, error) {
	return Principal{Kind: KeyRoot, Scopes: NewScopeSet(
		ScopePlatformKeysCreate,
		ScopePlatformKeysList,
		ScopePlatformKeysRotate,
		ScopePlatformKeysRevoke,
	)}, nil
}
