package services

import (
	"context"
	"testing"
)

func TestContainerReadyRejectsMissingDatabase(t *testing.T) {
	if err := (&Container{}).Ready(context.Background()); err == nil {
		t.Fatal("Ready returned nil without a database")
	}
}
