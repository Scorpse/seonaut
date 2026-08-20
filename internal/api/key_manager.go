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

func (m KeyManager) issue(ctx context.Context, kind KeyKind, scopes []string, expiresAt *time.Time, rotatedFrom string) (IssuedKey, error) {
	issued, stored, err := m.newIssued(kind, scopes, expiresAt, rotatedFrom)
	if err != nil {
		return IssuedKey{}, err
	}
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
	}
}
