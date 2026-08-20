package api

import (
	"context"
	"testing"
	"time"
)

type managerMemoryStore struct {
	keys map[string]StoredKey
}

func (s *managerMemoryStore) FindAPIKey(_ context.Context, publicID string) (StoredKey, error) {
	key, ok := s.keys[publicID]
	if !ok {
		return StoredKey{}, ErrKeyNotFound
	}
	return key, nil
}

func (s *managerMemoryStore) CreateAPIKey(_ context.Context, key StoredKey) error {
	s.keys[key.PublicID] = key
	return nil
}

func (s *managerMemoryStore) ListAPIKeys(_ context.Context, kind KeyKind) ([]StoredKey, error) {
	keys := []StoredKey{}
	for _, key := range s.keys {
		if key.Kind == kind {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *managerMemoryStore) RotateAPIKey(_ context.Context, oldPublicID string, replacement StoredKey, overlapUntil time.Time) error {
	old, ok := s.keys[oldPublicID]
	if !ok {
		return ErrKeyNotFound
	}
	old.ExpiresAt = &overlapUntil
	s.keys[oldPublicID] = old
	s.keys[replacement.PublicID] = replacement
	return nil
}

func (s *managerMemoryStore) RevokeAPIKey(_ context.Context, publicID string, kind KeyKind, at time.Time) error {
	key, ok := s.keys[publicID]
	if !ok || key.Kind != kind {
		return ErrKeyNotFound
	}
	key.RevokedAt = &at
	s.keys[publicID] = key
	return nil
}

func TestKeyManagerStoresOnlyAHashAndIssuedPlatformKeyAuthenticates(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := &managerMemoryStore{keys: map[string]StoredKey{}}
	manager := KeyManager{Environment: "prod", Store: store, Now: func() time.Time { return now }, RotationOverlap: 5 * time.Minute}

	issued, err := manager.CreatePlatformKey(context.Background(), CreateKeyInput{Scopes: []string{ScopeTenantsProvision, ScopeMetaRead}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAPIKey(issued.Key)
	if err != nil {
		t.Fatal(err)
	}
	stored := store.keys[parsed.PublicID]
	if stored.SecretHash == "" || stored.SecretHash == parsed.Secret {
		t.Fatalf("secret was not one-way stored: %#v", stored)
	}
	auth := Authenticator{Environment: "prod", Store: store, Now: func() time.Time { return now }}
	principal, err := auth.Authenticate(context.Background(), "Bearer "+issued.Key)
	if err != nil {
		t.Fatal(err)
	}
	if principal.Kind != KeyPlatform || !principal.Scopes.Has(ScopeTenantsProvision) {
		t.Fatalf("principal = %#v", principal)
	}
}

func TestKeyManagerRotationOverlapsThenExpiresOldKeyAndRevocationIsImmediate(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	store := &managerMemoryStore{keys: map[string]StoredKey{}}
	manager := KeyManager{Environment: "prod", Store: store, Now: func() time.Time { return now }, RotationOverlap: 5 * time.Minute}
	old, err := manager.CreatePlatformKey(context.Background(), CreateKeyInput{Scopes: []string{ScopeTenantsProvision}})
	if err != nil {
		t.Fatal(err)
	}
	oldParsed, _ := ParseAPIKey(old.Key)
	replacement, err := manager.RotatePlatformKey(context.Background(), oldParsed.PublicID)
	if err != nil {
		t.Fatal(err)
	}

	auth := Authenticator{Environment: "prod", Store: store, Now: func() time.Time { return now.Add(4 * time.Minute) }}
	if _, err := auth.Authenticate(context.Background(), "Bearer "+old.Key); err != nil {
		t.Fatalf("old key rejected during overlap: %v", err)
	}
	auth.Now = func() time.Time { return now.Add(6 * time.Minute) }
	if _, err := auth.Authenticate(context.Background(), "Bearer "+old.Key); err == nil {
		t.Fatal("old key remained active after overlap")
	}
	if _, err := auth.Authenticate(context.Background(), "Bearer "+replacement.Key); err != nil {
		t.Fatalf("replacement rejected: %v", err)
	}

	replacementParsed, _ := ParseAPIKey(replacement.Key)
	if err := manager.RevokePlatformKey(context.Background(), replacementParsed.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.Authenticate(context.Background(), "Bearer "+replacement.Key); err == nil {
		t.Fatal("revoked key remained active")
	}
}
