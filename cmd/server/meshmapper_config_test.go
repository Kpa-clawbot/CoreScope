package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestMeshMapperAPIUrl_Default(t *testing.T) {
	cfg := &Config{}
	if got := cfg.MeshMapperAPIUrl(); got != "https://meshmapper.net/api/coverage" {
		t.Errorf("want default URL, got %q", got)
	}
}

func TestMeshMapperAPIUrl_Config(t *testing.T) {
	cfg := &Config{MeshMapper: &MeshMapperConfig{APIUrl: "https://custom.example/cov"}}
	if got := cfg.MeshMapperAPIUrl(); got != "https://custom.example/cov" {
		t.Errorf("want custom URL, got %q", got)
	}
}

func TestMeshMapperAPIUrl_EnvOverride(t *testing.T) {
	os.Setenv("MESHMAPPER_API_URL", "https://env.example/cov")
	defer os.Unsetenv("MESHMAPPER_API_URL")
	cfg := &Config{}
	if got := cfg.MeshMapperAPIUrl(); got != "https://env.example/cov" {
		t.Errorf("want env URL, got %q", got)
	}
}

func TestMeshMapperAPIKey_Empty(t *testing.T) {
	cfg := &Config{}
	if got := cfg.MeshMapperAPIKey(); got != "" {
		t.Errorf("want empty key, got %q", got)
	}
}

func TestMeshMapperAPIKey_Config(t *testing.T) {
	cfg := &Config{MeshMapper: &MeshMapperConfig{APIKey: "secret123"}}
	if got := cfg.MeshMapperAPIKey(); got != "secret123" {
		t.Errorf("want secret123, got %q", got)
	}
}

func TestMeshMapperAPIKey_EnvOverride(t *testing.T) {
	os.Setenv("MESHMAPPER_API_KEY", "envkey")
	defer os.Unsetenv("MESHMAPPER_API_KEY")
	cfg := &Config{}
	if got := cfg.MeshMapperAPIKey(); got != "envkey" {
		t.Errorf("want envkey, got %q", got)
	}
}

func TestMeshMapperCacheTTL_Default(t *testing.T) {
	cfg := &Config{}
	if got := cfg.MeshMapperCacheTTL(); got != 300*time.Second {
		t.Errorf("want 300s, got %v", got)
	}
}

func TestMeshMapperCacheTTL_Config(t *testing.T) {
	cfg := &Config{MeshMapper: &MeshMapperConfig{CacheTTLSecs: 60}}
	if got := cfg.MeshMapperCacheTTL(); got != 60*time.Second {
		t.Errorf("want 60s, got %v", got)
	}
}

func TestMeshMapperCacheTTL_EnvOverride(t *testing.T) {
	os.Setenv("MESHMAPPER_CACHE_TTL_SECONDS", "120")
	defer os.Unsetenv("MESHMAPPER_CACHE_TTL_SECONDS")
	cfg := &Config{}
	if got := cfg.MeshMapperCacheTTL(); got != 120*time.Second {
		t.Errorf("want 120s, got %v", got)
	}
}

func TestConfigClientMeshMapperConfigured_ConfigOnly(t *testing.T) {
	os.Setenv("MESHMAPPER_API_KEY", "envkey")
	defer os.Unsetenv("MESHMAPPER_API_KEY")

	s := &Server{cfg: &Config{}}
	req := httptest.NewRequest(http.MethodGet, "/api/config/client", nil)
	rr := httptest.NewRecorder()
	s.handleConfigClient(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp ClientConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.MeshMapperConfigured {
		t.Fatal("meshMapperConfigured must ignore MESHMAPPER_API_KEY and require config.json meshMapper.apiKey")
	}
}

func TestConfigClientMeshMapperConfigured_WhenConfigHasKey(t *testing.T) {
	s := &Server{cfg: &Config{MeshMapper: &MeshMapperConfig{APIKey: "configured"}}}
	req := httptest.NewRequest(http.MethodGet, "/api/config/client", nil)
	rr := httptest.NewRecorder()
	s.handleConfigClient(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp ClientConfigResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.MeshMapperConfigured {
		t.Fatal("meshMapperConfigured should be true when config.json meshMapper.apiKey is set")
	}
}
