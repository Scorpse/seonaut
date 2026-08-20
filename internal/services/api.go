package services

import (
	"time"

	"github.com/stjudewashere/seonaut/internal/api"
	"github.com/stjudewashere/seonaut/internal/config"
)

func NewAPIServices(cfg *config.APIConfig, store api.MutableKeyStore) (api.Authenticator, api.KeyManager) {
	if cfg == nil {
		cfg = &config.APIConfig{Environment: "dev", RootPublicID: "root", RotationOverlapSeconds: 300}
	}
	overlap := time.Duration(cfg.RotationOverlapSeconds) * time.Second
	if overlap <= 0 {
		overlap = 5 * time.Minute
	}
	return api.Authenticator{
			Environment:  cfg.Environment,
			RootPublicID: cfg.RootPublicID,
			RootHash:     cfg.RootHash,
			Store:        store,
		}, api.KeyManager{
			Environment:     cfg.Environment,
			Store:           store,
			RotationOverlap: overlap,
		}
}
