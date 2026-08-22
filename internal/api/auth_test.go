package api

import (
	"context"
	"errors"
	"testing"
	"time"
)

type memoryKeyStore map[string]StoredKey

func (s memoryKeyStore) FindAPIKey(_ context.Context, publicID string) (StoredKey, error) {
	key, ok := s[publicID]
	if !ok {
		return StoredKey{}, ErrKeyNotFound
	}
	return key, nil
}

func TestParseAPIKeyRejectsAmbiguousOrMalformedCredentials(t *testing.T) {
	tests := []struct {
		input string
		ok    bool
	}{
		{input: "snk_prod_ab12.secret", ok: true},
		{input: "snk_dev_root.long-secret", ok: true},
		{input: ""},
		{input: "ab12.secret"},
		{input: "snk_prod_.secret"},
		{input: "snk_prod_ab12."},
		{input: "snk_prod_ab12.secret.extra"},
		{input: "snk_bad env_ab12.secret"},
	}

	for _, tt := range tests {
		_, err := ParseAPIKey(tt.input)
		if (err == nil) != tt.ok {
			t.Fatalf("ParseAPIKey(%q) error = %v, want ok=%t", tt.input, err, tt.ok)
		}
	}

	parsed, err := ParseAPIKey("snk_prod_ab12.secret")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Environment != "prod" || parsed.PublicID != "ab12" || parsed.Secret != "secret" {
		t.Fatalf("parsed = %#v", parsed)
	}
}

func TestSecretHashVerifiesOnlyTheOriginalSecret(t *testing.T) {
	hash, err := HashSecret("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifySecret(hash, "correct horse battery staple") {
		t.Fatal("original secret did not verify")
	}
	if VerifySecret(hash, "wrong secret") {
		t.Fatal("wrong secret verified")
	}
	if VerifySecret("not-a-phc-hash", "correct horse battery staple") {
		t.Fatal("malformed hash verified")
	}
}

func TestAuthenticatorReturnsBoundPrincipalForActiveDatabaseKey(t *testing.T) {
	hash, err := HashSecret("tenant-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := Authenticator{
		Environment: "prod",
		Store: memoryKeyStore{
			"tenant01": {
				PublicID:   "tenant01",
				SecretHash: hash,
				Kind:       KeyTenant,
				TenantID:   "tenant-a",
				Scopes:     []string{ScopeMetaRead, ScopeProjectsRead},
			},
		},
		Now: func() time.Time { return time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC) },
	}

	principal, err := auth.Authenticate(context.Background(), "Bearer snk_prod_tenant01.tenant-secret")
	if err != nil {
		t.Fatal(err)
	}
	if principal.Kind != KeyTenant || principal.KeyID != "tenant01" || principal.TenantID != "tenant-a" || !principal.Scopes.Has(ScopeProjectsRead) {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestAuthenticatorRejectsInactiveForeignAndInvalidKeysWithoutAnOracle(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	hash, err := HashSecret("secret")
	if err != nil {
		t.Fatal(err)
	}
	revoked := now.Add(-time.Minute)
	expired := now.Add(-time.Second)
	store := memoryKeyStore{
		"revoked": {PublicID: "revoked", SecretHash: hash, Kind: KeyTenant, RevokedAt: &revoked},
		"expired": {PublicID: "expired", SecretHash: hash, Kind: KeyTenant, ExpiresAt: &expired},
		"active":  {PublicID: "active", SecretHash: hash, Kind: KeyTenant},
	}
	auth := Authenticator{Environment: "prod", Store: store, Now: func() time.Time { return now }}

	for _, authorization := range []string{
		"",
		"Basic snk_prod_active.secret",
		"Bearer snk_stage_active.secret",
		"Bearer snk_prod_missing.secret",
		"Bearer snk_prod_active.wrong",
		"Bearer snk_prod_revoked.secret",
		"Bearer snk_prod_expired.secret",
	} {
		_, err := auth.Authenticate(context.Background(), authorization)
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Authenticate(%q) error = %v, want ErrUnauthenticated", authorization, err)
		}
	}
}

func TestRootAuthenticationProducesOnlyRootKeyManagementScopes(t *testing.T) {
	hash, err := HashSecret("root-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth := Authenticator{
		Environment:  "prod",
		RootPublicID: "root",
		RootHash:     hash,
		Now:          time.Now,
	}

	principal, err := auth.Authenticate(context.Background(), "Bearer snk_prod_root.root-secret")
	if err != nil {
		t.Fatal(err)
	}
	if principal.Kind != KeyRoot || !principal.Scopes.Has(ScopePlatformKeysCreate) || !principal.Scopes.Has(ScopePlatformKeysRevoke) {
		t.Fatalf("root principal = %#v", principal)
	}
	for _, forbidden := range []string{ScopeMetaRead, ScopeProjectsRead, ScopeCrawlsRun, ScopeFindingsRead} {
		if principal.Scopes.Has(forbidden) {
			t.Fatalf("root principal unexpectedly has %q", forbidden)
		}
	}
}
