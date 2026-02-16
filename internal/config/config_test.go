package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeConfigFile creates a temporary YAML config file and returns its path.
func writeConfigFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		t.Fatalf("writing temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

// minimalValidConfig returns a minimal valid YAML config string.
func minimalValidConfig() string {
	return `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test-key

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`
}

func TestLoad_ValidConfig(t *testing.T) {
	path := writeConfigFile(t, `
server:
  listen: "127.0.0.1:8080"
  request_timeout: 60s
  streaming_timeout: 120s
  max_request_body_mb: 5
  session_secret: "test-secret-1234567890abcdef1234567890abcdef"

database:
  path: "/tmp/test.db"
  retention_days: 30

pricing_lookup:
  url: "https://pricing.example.com"
  refresh_interval: 1h

circuit_breaker:
  enabled: true
  error_threshold: 0.3
  min_requests: 10
  cooldown_duration: 60s

retry:
  initial_backoff: 200ms
  max_backoff: 10s
  multiplier: 3.0

debug:
  enabled: true
  max_body_bytes: 65536
  retention_days: 60

cors:
  enabled: true
  allowed_origins:
    - "https://example.com"
  allowed_methods:
    - GET
    - POST
  allowed_headers:
    - Authorization
  max_age: 600

providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-test-key
    priority: 2
    timeout: 60s
  - name: anthropic
    base_url: https://api.anthropic.com/v1
    api_key: sk-ant-test
    priority: 1
    timeout: 30s

models:
  - name: gpt-4
    aliases:
      - gpt4
    routes:
      - provider: openai
        model: gpt-4
        pricing:
          input: 30.0
          output: 60.0
    load_balancing: first
  - name: claude-3
    routes:
      - provider: anthropic
        model: claude-3-opus-20240229

tokens:
  - name: test-token
    key: tok-abc123
    rate_limit_rpm: 100
    daily_budget_usd: 10.0
    max_concurrent: 5
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	// Server
	if cfg.Server.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q, want %q", cfg.Server.Listen, "127.0.0.1:8080")
	}
	if cfg.Server.RequestTimeout != 60*time.Second {
		t.Errorf("RequestTimeout = %v, want %v", cfg.Server.RequestTimeout, 60*time.Second)
	}
	if cfg.Server.StreamingTimeout != 120*time.Second {
		t.Errorf("StreamingTimeout = %v, want %v", cfg.Server.StreamingTimeout, 120*time.Second)
	}
	if cfg.Server.MaxRequestBodyMB != 5 {
		t.Errorf("MaxRequestBodyMB = %d, want %d", cfg.Server.MaxRequestBodyMB, 5)
	}
	if cfg.Server.SessionSecret != "test-secret-1234567890abcdef1234567890abcdef" {
		t.Errorf("SessionSecret = %q, want %q", cfg.Server.SessionSecret, "test-secret-1234567890abcdef1234567890abcdef")
	}

	// Database
	if cfg.Database.Path != "/tmp/test.db" {
		t.Errorf("Database.Path = %q, want %q", cfg.Database.Path, "/tmp/test.db")
	}
	if cfg.Database.RetentionDays != 30 {
		t.Errorf("Database.RetentionDays = %d, want %d", cfg.Database.RetentionDays, 30)
	}

	// Pricing
	if cfg.Pricing.URL != "https://pricing.example.com" {
		t.Errorf("Pricing.URL = %q, want %q", cfg.Pricing.URL, "https://pricing.example.com")
	}
	if cfg.Pricing.RefreshInterval != 1*time.Hour {
		t.Errorf("Pricing.RefreshInterval = %v, want %v", cfg.Pricing.RefreshInterval, 1*time.Hour)
	}

	// Circuit breaker
	if !cfg.CircuitBreaker.Enabled {
		t.Error("CircuitBreaker.Enabled = false, want true")
	}
	if cfg.CircuitBreaker.ErrorThreshold != 0.3 {
		t.Errorf("CircuitBreaker.ErrorThreshold = %v, want %v", cfg.CircuitBreaker.ErrorThreshold, 0.3)
	}
	if cfg.CircuitBreaker.MinRequests != 10 {
		t.Errorf("CircuitBreaker.MinRequests = %d, want %d", cfg.CircuitBreaker.MinRequests, 10)
	}
	if cfg.CircuitBreaker.CooldownDuration != 60*time.Second {
		t.Errorf("CircuitBreaker.CooldownDuration = %v, want %v", cfg.CircuitBreaker.CooldownDuration, 60*time.Second)
	}

	// Retry
	if cfg.Retry.InitialBackoff != 200*time.Millisecond {
		t.Errorf("Retry.InitialBackoff = %v, want %v", cfg.Retry.InitialBackoff, 200*time.Millisecond)
	}
	if cfg.Retry.MaxBackoff != 10*time.Second {
		t.Errorf("Retry.MaxBackoff = %v, want %v", cfg.Retry.MaxBackoff, 10*time.Second)
	}
	if cfg.Retry.Multiplier != 3.0 {
		t.Errorf("Retry.Multiplier = %v, want %v", cfg.Retry.Multiplier, 3.0)
	}

	// Debug
	if !cfg.Debug.Enabled {
		t.Error("Debug.Enabled = false, want true")
	}
	if cfg.Debug.MaxBodyBytes != 65536 {
		t.Errorf("Debug.MaxBodyBytes = %d, want %d", cfg.Debug.MaxBodyBytes, 65536)
	}
	if cfg.Debug.RetentionDays != 60 {
		t.Errorf("Debug.RetentionDays = %d, want %d", cfg.Debug.RetentionDays, 60)
	}

	// CORS
	if !cfg.CORS.Enabled {
		t.Error("CORS.Enabled = false, want true")
	}
	if cfg.CORS.MaxAge != 600 {
		t.Errorf("CORS.MaxAge = %d, want %d", cfg.CORS.MaxAge, 600)
	}

	// Providers
	if len(cfg.Providers) != 2 {
		t.Fatalf("len(Providers) = %d, want 2", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "openai" {
		t.Errorf("Providers[0].Name = %q, want %q", cfg.Providers[0].Name, "openai")
	}
	if cfg.Providers[0].Timeout != 60*time.Second {
		t.Errorf("Providers[0].Timeout = %v, want %v", cfg.Providers[0].Timeout, 60*time.Second)
	}

	// Models
	if len(cfg.Models) != 2 {
		t.Fatalf("len(Models) = %d, want 2", len(cfg.Models))
	}
	if cfg.Models[0].LoadBalancing != "first" {
		t.Errorf("Models[0].LoadBalancing = %q, want %q", cfg.Models[0].LoadBalancing, "first")
	}
	if len(cfg.Models[0].Aliases) != 1 || cfg.Models[0].Aliases[0] != "gpt4" {
		t.Errorf("Models[0].Aliases = %v, want [gpt4]", cfg.Models[0].Aliases)
	}
	if cfg.Models[0].Routes[0].Pricing.Input != 30.0 {
		t.Errorf("Models[0].Routes[0].Pricing.Input = %v, want 30.0", cfg.Models[0].Routes[0].Pricing.Input)
	}

	// Tokens
	if len(cfg.Tokens) != 1 {
		t.Fatalf("len(Tokens) = %d, want 1", len(cfg.Tokens))
	}
	if cfg.Tokens[0].Name != "test-token" {
		t.Errorf("Tokens[0].Name = %q, want %q", cfg.Tokens[0].Name, "test-token")
	}
	if cfg.Tokens[0].RateLimitRPM != 100 {
		t.Errorf("Tokens[0].RateLimitRPM = %d, want 100", cfg.Tokens[0].RateLimitRPM)
	}
}

func TestValidate_Defaults(t *testing.T) {
	path := writeConfigFile(t, minimalValidConfig())
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	tests := []struct {
		name string
		got  any
		want any
	}{
		// Server defaults
		{"Server.Listen", cfg.Server.Listen, "0.0.0.0:3000"},
		{"Server.RequestTimeout", cfg.Server.RequestTimeout, 300 * time.Second},
		{"Server.StreamingTimeout", cfg.Server.StreamingTimeout, 5 * time.Minute},
		{"Server.MaxRequestBodyMB", cfg.Server.MaxRequestBodyMB, 10},

		// Database defaults
		{"Database.Path", cfg.Database.Path, "gateway.db"},
		{"Database.RetentionDays", cfg.Database.RetentionDays, 90},

		// Pricing defaults
		{"Pricing.RefreshInterval", cfg.Pricing.RefreshInterval, 6 * time.Hour},

		// Circuit breaker defaults
		{"CircuitBreaker.ErrorThreshold", cfg.CircuitBreaker.ErrorThreshold, 0.5},
		{"CircuitBreaker.MinRequests", cfg.CircuitBreaker.MinRequests, 5},
		{"CircuitBreaker.CooldownDuration", cfg.CircuitBreaker.CooldownDuration, 30 * time.Second},

		// Retry defaults
		{"Retry.InitialBackoff", cfg.Retry.InitialBackoff, 100 * time.Millisecond},
		{"Retry.MaxBackoff", cfg.Retry.MaxBackoff, 5 * time.Second},
		{"Retry.Multiplier", cfg.Retry.Multiplier, 2.0},

		// Debug defaults
		{"Debug.MaxBodyBytes", cfg.Debug.MaxBodyBytes, 0},
		{"Debug.RetentionDays", cfg.Debug.RetentionDays, 90},

		// CORS defaults
		{"CORS.MaxAge", cfg.CORS.MaxAge, 300},

		// Provider defaults
		{"Providers[0].Timeout", cfg.Providers[0].Timeout, 120 * time.Second},
		{"Providers[0].Priority", cfg.Providers[0].Priority, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compare using formatted strings to handle different numeric types
			gotStr := formatValue(tt.got)
			wantStr := formatValue(tt.want)
			if gotStr != wantStr {
				t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
			}
		})
	}

	// SessionSecret should be auto-generated (non-empty, 64 hex chars)
	if cfg.Server.SessionSecret == "" {
		t.Error("SessionSecret should be auto-generated when empty")
	}
	if len(cfg.Server.SessionSecret) != 64 {
		t.Errorf("SessionSecret length = %d, want 64 hex chars", len(cfg.Server.SessionSecret))
	}
}

func formatValue(v any) string {
	switch val := v.(type) {
	case time.Duration:
		return val.String()
	case float64:
		return fmt.Sprintf("%g", val)
	case int:
		return fmt.Sprintf("%d", val)
	case string:
		return val
	default:
		return fmt.Sprintf("%v", val)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Fatal("Load() should return error for nonexistent file")
	}
}

func TestLoad_InvalidYAML(t *testing.T) {
	path := writeConfigFile(t, `{{{invalid yaml`)
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() should return error for invalid YAML")
	}
}

func TestValidate_DuplicateProviderNames(t *testing.T) {
	path := writeConfigFile(t, `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key1
  - name: openai
    base_url: https://api.openai.com/v2
    api_key: sk-key2

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() should return error for duplicate provider names")
	}
	wantSubstr := "duplicate provider name: openai"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want to contain %q", err.Error(), wantSubstr)
	}
}

func TestValidate_DuplicateModelNames(t *testing.T) {
	path := writeConfigFile(t, `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key1

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4-turbo
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() should return error for duplicate model names")
	}
	wantSubstr := "duplicate model name: gpt-4"
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error = %q, want to contain %q", err.Error(), wantSubstr)
	}
}

func TestValidate_AliasConflicts(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name: "alias conflicts with model name",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key1

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
  - name: claude-3
    aliases:
      - gpt-4
    routes:
      - provider: openai
        model: claude-3
`,
			wantErr: "model alias gpt-4 conflicts with existing model or alias",
		},
		{
			name: "alias conflicts with another alias",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key1

models:
  - name: gpt-4
    aliases:
      - fast-model
    routes:
      - provider: openai
        model: gpt-4
  - name: claude-3
    aliases:
      - fast-model
    routes:
      - provider: openai
        model: claude-3
`,
			wantErr: "model alias fast-model conflicts with existing model or alias",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, tt.config)
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load() should return error for alias conflicts")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidate_MissingRequiredFields(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		wantErr string
	}{
		{
			name:    "no providers",
			config:  `models: [{name: test, routes: [{provider: x, model: y}]}]`,
			wantErr: "at least one provider is required",
		},
		{
			name: "no models",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key
`,
			wantErr: "at least one model is required",
		},
		{
			name: "provider missing name",
			config: `
providers:
  - base_url: https://api.openai.com/v1
    api_key: sk-key

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`,
			wantErr: "provider 0: name, base_url, and api_key are required",
		},
		{
			name: "provider missing base_url",
			config: `
providers:
  - name: openai
    api_key: sk-key

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`,
			wantErr: "provider 0: name, base_url, and api_key are required",
		},
		{
			name: "provider missing api_key",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`,
			wantErr: "provider 0: name, base_url, and api_key are required",
		},
		{
			name: "model missing name",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key

models:
  - routes:
      - provider: openai
        model: gpt-4
`,
			wantErr: "model 0: name is required",
		},
		{
			name: "model missing routes",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key

models:
  - name: gpt-4
`,
			wantErr: "model gpt-4: at least one route is required",
		},
		{
			name: "route missing provider",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key

models:
  - name: gpt-4
    routes:
      - model: gpt-4
`,
			wantErr: "model gpt-4 route 0: provider and model are required",
		},
		{
			name: "route missing model",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key

models:
  - name: gpt-4
    routes:
      - provider: openai
`,
			wantErr: "model gpt-4 route 0: provider and model are required",
		},
		{
			name: "route references unknown provider",
			config: `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-key

models:
  - name: gpt-4
    routes:
      - provider: anthropic
        model: gpt-4
`,
			wantErr: "model gpt-4 route 0: unknown provider anthropic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeConfigFile(t, tt.config)
			_, err := Load(path)
			if err == nil {
				t.Fatalf("Load() should return error, want %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestProviderByName(t *testing.T) {
	path := writeConfigFile(t, `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: sk-openai
  - name: anthropic
    base_url: https://api.anthropic.com/v1
    api_key: sk-anthropic

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	tests := []struct {
		name     string
		lookup   string
		wantNil  bool
		wantName string
	}{
		{"existing provider openai", "openai", false, "openai"},
		{"existing provider anthropic", "anthropic", false, "anthropic"},
		{"nonexistent provider", "google", true, ""},
		{"empty name", "", true, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := cfg.ProviderByName(tt.lookup)
			if tt.wantNil {
				if p != nil {
					t.Errorf("ProviderByName(%q) = %v, want nil", tt.lookup, p)
				}
			} else {
				if p == nil {
					t.Fatalf("ProviderByName(%q) = nil, want provider", tt.lookup)
				}
				if p.Name != tt.wantName {
					t.Errorf("ProviderByName(%q).Name = %q, want %q", tt.lookup, p.Name, tt.wantName)
				}
			}
		})
	}

	// Verify returned pointer refers to the actual config entry
	p := cfg.ProviderByName("openai")
	if p.APIKey != "sk-openai" {
		t.Errorf("ProviderByName(openai).APIKey = %q, want %q", p.APIKey, "sk-openai")
	}
}

func TestLoad_EnvironmentVariableExpansion(t *testing.T) {
	t.Setenv("TEST_API_KEY", "sk-from-env")
	t.Setenv("TEST_BASE_URL", "https://env.example.com/v1")
	t.Setenv("TEST_PROVIDER_NAME", "env-provider")

	path := writeConfigFile(t, `
providers:
  - name: $TEST_PROVIDER_NAME
    base_url: ${TEST_BASE_URL}
    api_key: ${TEST_API_KEY}

models:
  - name: test-model
    routes:
      - provider: env-provider
        model: gpt-4
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Providers[0].Name != "env-provider" {
		t.Errorf("Provider.Name = %q, want %q", cfg.Providers[0].Name, "env-provider")
	}
	if cfg.Providers[0].BaseURL != "https://env.example.com/v1" {
		t.Errorf("Provider.BaseURL = %q, want %q", cfg.Providers[0].BaseURL, "https://env.example.com/v1")
	}
	if cfg.Providers[0].APIKey != "sk-from-env" {
		t.Errorf("Provider.APIKey = %q, want %q", cfg.Providers[0].APIKey, "sk-from-env")
	}
}

func TestLoad_UnsetEnvVarExpandsToEmpty(t *testing.T) {
	// Ensure the variable is not set
	t.Setenv("TEST_UNSET_VAR", "")
	os.Unsetenv("TEST_UNSET_VAR")

	path := writeConfigFile(t, `
providers:
  - name: openai
    base_url: https://api.openai.com/v1
    api_key: ${TEST_UNSET_VAR}

models:
  - name: gpt-4
    routes:
      - provider: openai
        model: gpt-4
`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load() should return error when env var expands to empty required field")
	}
	// api_key will be empty, so validation should catch it
	if !strings.Contains(err.Error(), "api_key are required") {
		t.Errorf("error = %q, want to contain api_key validation message", err.Error())
	}
}
