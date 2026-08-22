package config_test

import (
	"testing"

	"github.com/stjudewashere/seonaut/internal/config"
)

func TestLoadConfig(t *testing.T) {
	config, err := config.NewConfig("./testdata/config")
	if err != nil {
		t.Fatalf("Error loading config file: %v", err)
	}

	pm := []struct {
		input int
		want  int
	}{
		{config.HTTPServer.Port, 9000},
		{config.DB.Port, 3306},
	}

	for _, pv := range pm {
		if pv.input != pv.want {
			t.Errorf("%d != %d\n", pv.input, pv.want)
		}
	}

	m := []struct {
		input string
		want  string
	}{
		{config.HTTPServer.Server, "example.com"},
		{config.DB.Server, "dbexample.com"},
		{config.DB.User, "root"},
		{config.DB.Pass, "root"},
		{config.DB.Name, "test"},
		{config.Crawler.Agent, "testing"},
		{config.API.Environment, "test"},
		{config.API.RootPublicID, "root"},
		{config.API.RootHash, "test-hash"},
		{config.API.CursorSecret, "0123456789abcdef0123456789abcdef"},
	}

	for _, v := range m {
		if v.input != v.want {
			t.Errorf("%s != %s\n", v.input, v.want)
		}
	}

	if config.API.RotationOverlapSeconds != 120 {
		t.Fatalf("rotation overlap = %d, want 120", config.API.RotationOverlapSeconds)
	}
}
