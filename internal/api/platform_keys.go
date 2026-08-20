package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

type CreateKeyInput struct {
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

type KeyMetadata struct {
	PublicID    string     `json:"public_id"`
	Kind        KeyKind    `json:"kind"`
	Scopes      []string   `json:"scopes"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
	RotatedFrom string     `json:"rotated_from,omitempty"`
}

type IssuedKey struct {
	Key string `json:"key"`
	KeyMetadata
}

type PlatformKeyService interface {
	CreatePlatformKey(context.Context, CreateKeyInput) (IssuedKey, error)
	ListPlatformKeys(context.Context) ([]KeyMetadata, error)
	RotatePlatformKey(context.Context, string) (IssuedKey, error)
	RevokePlatformKey(context.Context, string) error
}

func (s *server) createPlatformKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(w, r, ScopePlatformKeysCreate) {
		return
	}
	var input CreateKeyInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || !validPlatformScopes(input.Scopes) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "The platform key request is invalid")
		return
	}
	if s.deps.PlatformKeys == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Key management is unavailable")
		return
	}
	issued, err := s.deps.PlatformKeys.CreatePlatformKey(r.Context(), input)
	if err != nil {
		writeKeyServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": issued, "request_id": requestIDFrom(r.Context())})
}

func (s *server) listPlatformKeys(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(w, r, ScopePlatformKeysList) {
		return
	}
	if s.deps.PlatformKeys == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Key management is unavailable")
		return
	}
	keys, err := s.deps.PlatformKeys.ListPlatformKeys(r.Context())
	if err != nil {
		writeKeyServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": keys, "request_id": requestIDFrom(r.Context())})
}

func (s *server) rotatePlatformKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(w, r, ScopePlatformKeysRotate) {
		return
	}
	if s.deps.PlatformKeys == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Key management is unavailable")
		return
	}
	issued, err := s.deps.PlatformKeys.RotatePlatformKey(r.Context(), r.PathValue("key_id"))
	if err != nil {
		writeKeyServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": issued, "request_id": requestIDFrom(r.Context())})
}

func (s *server) revokePlatformKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireRoot(w, r, ScopePlatformKeysRevoke) {
		return
	}
	if s.deps.PlatformKeys == nil {
		writeError(w, r, http.StatusServiceUnavailable, "service_unavailable", "Key management is unavailable")
		return
	}
	if err := s.deps.PlatformKeys.RevokePlatformKey(r.Context(), r.PathValue("key_id")); err != nil {
		writeKeyServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) requireRoot(w http.ResponseWriter, r *http.Request, scope string) bool {
	principal, err := s.authenticate(r)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "Authentication is required")
		return false
	}
	if principal.Kind != KeyRoot || !principal.Scopes.Has(scope) {
		writeError(w, r, http.StatusForbidden, "scope_forbidden", "The credential cannot access this resource")
		return false
	}
	return true
}

func validPlatformScopes(scopes []string) bool {
	if len(scopes) == 0 {
		return false
	}
	allowed := NewScopeSet(
		ScopeTenantsProvision,
		ScopeTenantKeysCreate,
		ScopeTenantKeysRotate,
		ScopeTenantKeysRevoke,
		ScopeMetaRead,
	)
	seen := ScopeSet{}
	for _, scope := range scopes {
		if !allowed.Has(scope) || seen.Has(scope) {
			return false
		}
		seen[scope] = struct{}{}
	}
	return true
}

func writeKeyServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, ErrKeyNotFound) {
		writeError(w, r, http.StatusNotFound, "key_not_found", "Key was not found")
		return
	}
	writeError(w, r, http.StatusInternalServerError, "internal_error", "The request could not be completed")
}
