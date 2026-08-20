package services

import (
	"context"
	"testing"
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/config"
)

type apiTestStore struct{}

func (apiTestStore) FindAPIKey(context.Context, string) (api.StoredKey, error) {
	return api.StoredKey{}, api.ErrKeyNotFound
}
func (apiTestStore) CreateAPIKey(context.Context, api.StoredKey) error { return nil }
func (apiTestStore) ListAPIKeys(context.Context, api.KeyKind) ([]api.StoredKey, error) {
	return nil, nil
}
func (apiTestStore) RotateAPIKey(context.Context, string, api.StoredKey, time.Time) error {
	return nil
}
func (apiTestStore) RevokeAPIKey(context.Context, string, api.KeyKind, time.Time) error {
	return nil
}

func TestNewAPIServicesCarriesConfigIntoAuthenticationAndRotation(t *testing.T) {
	hash, err := api.HashSecret("root-secret")
	if err != nil {
		t.Fatal(err)
	}
	auth, manager := NewAPIServices(&config.APIConfig{
		Environment:            "prod",
		RootPublicID:           "root",
		RootHash:               hash,
		RotationOverlapSeconds: 120,
	}, apiTestStore{})

	principal, err := auth.Authenticate(context.Background(), "Bearer snk_prod_root.root-secret")
	if err != nil || principal.Kind != api.KeyRoot {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
	if manager.Environment != "prod" || manager.RotationOverlap != 2*time.Minute {
		t.Fatalf("manager = %#v", manager)
	}
}
