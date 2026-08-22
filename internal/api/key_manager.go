package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"sort"
	"time"
)

var ErrInvalidKeyRequest = errors.New("invalid key request")

type MutableKeyStore interface {
	KeyStore
	CreateAPIKey(context.Context, StoredKey) error
	ListAPIKeys(context.Context, KeyKind) ([]StoredKey, error)
	RotateAPIKey(context.Context, string, StoredKey, time.Time) error
	RevokeAPIKey(context.Context, string, KeyKind, time.Time) error
}

type TenantMutableKeyStore interface {
	MutableKeyStore
	ListTenantAPIKeys(context.Context, string) ([]StoredKey, error)
	RotateTenantAPIKey(context.Context, string, string, StoredKey, time.Time) error
	RevokeTenantAPIKey(context.Context, string, string, time.Time) error
}

type ProjectBoundKeyStore interface {
	ProjectBelongsToTenant(context.Context, string, string) (bool, error)
}

type KeyManager struct {
	Environment     string
	Store           MutableKeyStore
	Now             func() time.Time
	RotationOverlap time.Duration
}

func (m KeyManager) CreatePlatformKey(ctx context.Context, input CreateKeyInput) (IssuedKey, error) {
	if m.Store == nil || m.Environment == "" || !validPlatformScopes(input.Scopes) {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	now := m.now()
	if input.ExpiresAt != nil && !input.ExpiresAt.After(now) {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	return m.issue(ctx, KeyPlatform, input.Scopes, input.ExpiresAt, "")
}

func (m KeyManager) ListPlatformKeys(ctx context.Context) ([]KeyMetadata, error) {
	if m.Store == nil {
		return nil, ErrInvalidKeyRequest
	}
	stored, err := m.Store.ListAPIKeys(ctx, KeyPlatform)
	if err != nil {
		return nil, err
	}
	keys := make([]KeyMetadata, 0, len(stored))
	for _, key := range stored {
		keys = append(keys, metadataFrom(key))
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].CreatedAt.Equal(keys[j].CreatedAt) {
			return keys[i].PublicID < keys[j].PublicID
		}
		return keys[i].CreatedAt.Before(keys[j].CreatedAt)
	})
	return keys, nil
}

func (m KeyManager) RotatePlatformKey(ctx context.Context, publicID string) (IssuedKey, error) {
	if m.Store == nil {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	old, err := m.Store.FindAPIKey(ctx, publicID)
	if err != nil || old.Kind != KeyPlatform || old.RevokedAt != nil {
		return IssuedKey{}, ErrKeyNotFound
	}
	replacement, stored, err := m.newIssued(KeyPlatform, old.Scopes, old.ExpiresAt, publicID)
	if err != nil {
		return IssuedKey{}, err
	}
	overlap := m.RotationOverlap
	if overlap <= 0 {
		overlap = 5 * time.Minute
	}
	if err := m.Store.RotateAPIKey(ctx, publicID, stored, m.now().Add(overlap)); err != nil {
		return IssuedKey{}, err
	}
	return replacement, nil
}

func (m KeyManager) RevokePlatformKey(ctx context.Context, publicID string) error {
	if m.Store == nil {
		return ErrInvalidKeyRequest
	}
	return m.Store.RevokeAPIKey(ctx, publicID, KeyPlatform, m.now())
}

func (m KeyManager) CreateTenantKey(ctx context.Context, tenantID string, input CreateKeyInput) (IssuedKey, error) {
	if tenantID == "" || !validTenantScopes(input.Scopes, KeyTenant) || input.ExpiresAt != nil && !input.ExpiresAt.After(m.now()) {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	return m.issueBound(ctx, KeyTenant, tenantID, "", input.Scopes, input.ExpiresAt, "")
}

func (m KeyManager) CreateDelegatedKey(ctx context.Context, issuer Principal, input DelegatedKeyInput) (IssuedKey, error) {
	if issuer.Kind != KeyTenant || issuer.TenantID == "" || !issuer.Scopes.Has(ScopeKeysManage) {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	if input.Kind != KeyTenant && input.Kind != KeyReadOnly || input.Kind == KeyTenant && input.ProjectID != "" || !validTenantScopes(input.Scopes, input.Kind) {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	if input.ProjectID != "" {
		store, ok := m.Store.(ProjectBoundKeyStore)
		if !ok {
			return IssuedKey{}, ErrInvalidKeyRequest
		}
		belongs, err := store.ProjectBelongsToTenant(ctx, issuer.TenantID, input.ProjectID)
		if err != nil {
			return IssuedKey{}, err
		}
		if !belongs {
			return IssuedKey{}, ErrInvalidKeyRequest
		}
	}
	return m.issueBound(ctx, input.Kind, issuer.TenantID, input.ProjectID, input.Scopes, input.ExpiresAt, "")
}

func (m KeyManager) ListTenantKeys(ctx context.Context, principal Principal) ([]KeyMetadata, error) {
	store, ok := m.Store.(TenantMutableKeyStore)
	if !ok || principal.Kind != KeyTenant || principal.TenantID == "" || !principal.Scopes.Has(ScopeKeysManage) {
		return nil, ErrInvalidKeyRequest
	}
	keys, err := store.ListTenantAPIKeys(ctx, principal.TenantID)
	if err != nil {
		return nil, err
	}
	out := make([]KeyMetadata, 0, len(keys))
	for _, key := range keys {
		out = append(out, metadataFrom(key))
	}
	return out, nil
}

func (m KeyManager) RotateTenantKey(ctx context.Context, principal Principal, publicID string) (IssuedKey, error) {
	store, ok := m.Store.(TenantMutableKeyStore)
	if !ok || principal.Kind != KeyTenant || principal.TenantID == "" || !principal.Scopes.Has(ScopeKeysManage) {
		return IssuedKey{}, ErrInvalidKeyRequest
	}
	old, err := store.FindAPIKey(ctx, publicID)
	if err != nil || old.TenantID != principal.TenantID || old.Kind != KeyTenant && old.Kind != KeyReadOnly || old.RevokedAt != nil {
		return IssuedKey{}, ErrKeyNotFound
	}
	issued, replacement, err := m.newIssued(old.Kind, old.Scopes, old.ExpiresAt, publicID)
	if err != nil {
		return IssuedKey{}, err
	}
	replacement.TenantID, replacement.ProjectID = old.TenantID, old.ProjectID
	overlap := m.RotationOverlap
	if overlap <= 0 {
		overlap = 5 * time.Minute
	}
	if err := store.RotateTenantAPIKey(ctx, principal.TenantID, publicID, replacement, m.now().Add(overlap)); err != nil {
		return IssuedKey{}, err
	}
	issued.TenantID, issued.ProjectID = old.TenantID, old.ProjectID
	return issued, nil
}

func (m KeyManager) RevokeTenantKey(ctx context.Context, principal Principal, publicID string) error {
	store, ok := m.Store.(TenantMutableKeyStore)
	if !ok || principal.Kind != KeyTenant || principal.TenantID == "" || !principal.Scopes.Has(ScopeKeysManage) {
		return ErrInvalidKeyRequest
	}
	return store.RevokeTenantAPIKey(ctx, principal.TenantID, publicID, m.now())
}

func (m KeyManager) issue(ctx context.Context, kind KeyKind, scopes []string, expiresAt *time.Time, rotatedFrom string) (IssuedKey, error) {
	return m.issueBound(ctx, kind, "", "", scopes, expiresAt, rotatedFrom)
}

func (m KeyManager) issueBound(ctx context.Context, kind KeyKind, tenantID, projectID string, scopes []string, expiresAt *time.Time, rotatedFrom string) (IssuedKey, error) {
	issued, stored, err := m.newIssued(kind, scopes, expiresAt, rotatedFrom)
	if err != nil {
		return IssuedKey{}, err
	}
	stored.TenantID, stored.ProjectID = tenantID, projectID
	issued.TenantID, issued.ProjectID = tenantID, projectID
	if err := m.Store.CreateAPIKey(ctx, stored); err != nil {
		return IssuedKey{}, err
	}
	return issued, nil
}

func (m KeyManager) newIssued(kind KeyKind, scopes []string, expiresAt *time.Time, rotatedFrom string) (IssuedKey, StoredKey, error) {
	publicRaw := make([]byte, 12)
	secretRaw := make([]byte, 32)
	if _, err := rand.Read(publicRaw); err != nil {
		return IssuedKey{}, StoredKey{}, err
	}
	if _, err := rand.Read(secretRaw); err != nil {
		return IssuedKey{}, StoredKey{}, err
	}
	publicID := hex.EncodeToString(publicRaw)
	secret := base64.RawURLEncoding.EncodeToString(secretRaw)
	secretHash, err := HashSecret(secret)
	if err != nil {
		return IssuedKey{}, StoredKey{}, err
	}
	createdAt := m.now()
	stored := StoredKey{
		PublicID:    publicID,
		SecretHash:  secretHash,
		Kind:        kind,
		Scopes:      append([]string(nil), scopes...),
		CreatedAt:   createdAt,
		ExpiresAt:   expiresAt,
		RotatedFrom: rotatedFrom,
	}
	issued := IssuedKey{
		Key:         "snk_" + m.Environment + "_" + publicID + "." + secret,
		KeyMetadata: metadataFrom(stored),
	}
	return issued, stored, nil
}

func (m KeyManager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func metadataFrom(key StoredKey) KeyMetadata {
	return KeyMetadata{
		PublicID:    key.PublicID,
		Kind:        key.Kind,
		Scopes:      append([]string(nil), key.Scopes...),
		CreatedAt:   key.CreatedAt,
		ExpiresAt:   key.ExpiresAt,
		RevokedAt:   key.RevokedAt,
		RotatedFrom: key.RotatedFrom,
		TenantID:    key.TenantID,
		ProjectID:   key.ProjectID,
	}
}

func validTenantScopes(scopes []string, kind KeyKind) bool {
	if len(scopes) == 0 {
		return false
	}
	allowed := NewScopeSet(ScopeProjectsRead, ScopeCrawlsRead, ScopeFindingsRead, ScopeExportsRead, ScopeMetaRead)
	if kind == KeyTenant {
		for _, scope := range []string{ScopeProjectsWrite, ScopeCrawlsRun, ScopeCrawlsCancel, ScopeKeysManage} {
			allowed[scope] = struct{}{}
		}
	} else if kind != KeyReadOnly {
		return false
	}
	seen := ScopeSet{}
	for _, scope := range scopes {
		if !allowed.Has(scope) || seen.Has(scope) {
			return false
		}
		seen[scope] = struct{}{}
	}
	return true
}
