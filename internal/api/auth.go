package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

var ErrKeyNotFound = errors.New("api key not found")

type ParsedAPIKey struct {
	Environment string
	PublicID    string
	Secret      string
}

type StoredKey struct {
	PublicID    string
	SecretHash  string
	Kind        KeyKind
	TenantID    string
	ProjectID   string
	Scopes      []string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	RevokedAt   *time.Time
	RotatedFrom string
}

type KeyStore interface {
	FindAPIKey(context.Context, string) (StoredKey, error)
}

type Authenticator struct {
	Environment  string
	RootPublicID string
	RootHash     string
	Store        KeyStore
	Now          func() time.Time
}

func (a Authenticator) Authenticate(ctx context.Context, authorization string) (Principal, error) {
	const bearer = "Bearer "
	if !strings.HasPrefix(authorization, bearer) || strings.TrimSpace(authorization) != authorization {
		return Principal{}, ErrUnauthenticated
	}
	parsed, err := ParseAPIKey(strings.TrimPrefix(authorization, bearer))
	if err != nil || parsed.Environment != a.Environment {
		return Principal{}, ErrUnauthenticated
	}

	if parsed.PublicID == a.RootPublicID && a.RootPublicID != "" {
		if !VerifySecret(a.RootHash, parsed.Secret) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{
			Kind:  KeyRoot,
			KeyID: parsed.PublicID,
			Scopes: NewScopeSet(
				ScopePlatformKeysCreate,
				ScopePlatformKeysList,
				ScopePlatformKeysRotate,
				ScopePlatformKeysRevoke,
			),
		}, nil
	}

	if a.Store == nil {
		return Principal{}, ErrUnauthenticated
	}
	stored, err := a.Store.FindAPIKey(ctx, parsed.PublicID)
	if err != nil || stored.PublicID != parsed.PublicID || !VerifySecret(stored.SecretHash, parsed.Secret) {
		return Principal{}, ErrUnauthenticated
	}
	now := time.Now()
	if a.Now != nil {
		now = a.Now()
	}
	if stored.RevokedAt != nil || stored.ExpiresAt != nil && !stored.ExpiresAt.After(now) {
		return Principal{}, ErrUnauthenticated
	}

	return Principal{
		Kind:      stored.Kind,
		KeyID:     stored.PublicID,
		TenantID:  stored.TenantID,
		ProjectID: stored.ProjectID,
		Scopes:    NewScopeSet(stored.Scopes...),
	}, nil
}

func ParseAPIKey(value string) (ParsedAPIKey, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[1] == "" {
		return ParsedAPIKey{}, ErrUnauthenticated
	}
	prefix := strings.Split(parts[0], "_")
	if len(prefix) != 3 || prefix[0] != "snk" || !keyToken(prefix[1]) || !keyToken(prefix[2]) {
		return ParsedAPIKey{}, ErrUnauthenticated
	}
	if strings.ContainsAny(parts[1], " \t\r\n") {
		return ParsedAPIKey{}, ErrUnauthenticated
	}
	return ParsedAPIKey{Environment: prefix[1], PublicID: prefix[2], Secret: parts[1]}, nil
}

func keyToken(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func HashSecret(secret string) (string, error) {
	const (
		memory      = 64 * 1024
		iterations  = 3
		parallelism = 2
		saltLength  = 16
		keyLength   = 32
	)
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(secret), salt, iterations, memory, parallelism, keyLength)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory,
		iterations,
		parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifySecret(encoded, secret string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	if memory == 0 || iterations == 0 || parallelism == 0 || memory > 1024*1024 || iterations > 20 || parallelism > 32 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) < 16 {
		return false
	}
	got := argon2.IDKey([]byte(secret), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
