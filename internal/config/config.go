package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server         ServerConfig         `yaml:"server"`
	Database       DatabaseConfig       `yaml:"database"`
	Cache          CacheConfig          `yaml:"cache"`
	Pricing        PricingLookupConfig  `yaml:"pricing_lookup"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
	Retry          RetryConfig          `yaml:"retry"`
	Debug          DebugConfig          `yaml:"debug"`
	CORS           CORSConfig           `yaml:"cors"`
	Dedup          DedupConfig          `yaml:"dedup"`
	Webhooks       []WebhookConfig      `yaml:"webhooks"`
	Providers      []ProviderConfig     `yaml:"providers"`
	Models         []ModelConfig        `yaml:"models"`
	Tokens         []TokenConfig        `yaml:"tokens"`
}

type DedupConfig struct {
	Enabled bool          `yaml:"enabled"`
	Window  time.Duration `yaml:"window"` // max time to wait for dedup result
}

type WebhookConfig struct {
	URL    string   `yaml:"url"`
	Events []string `yaml:"events"` // event types to send
	Secret string   `yaml:"secret"` // optional HMAC secret
}

type PricingLookupConfig struct {
	URL             string        `yaml:"url"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
}

type DefaultAdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type TokenConfig struct {
	Name             string  `yaml:"name"`
	Key              string  `yaml:"key"`
	RateLimitRPM     int     `yaml:"rate_limit_rpm"`     // 0 = unlimited
	DailyBudgetUSD   float64 `yaml:"daily_budget_usd"`   // 0 = unlimited
	MonthlyBudgetUSD float64 `yaml:"monthly_budget_usd"` // 0 = unlimited
	MaxConcurrent    int     `yaml:"max_concurrent"`     // 0 = unlimited
}

type ServerConfig struct {
	Listen           string             `yaml:"listen"`
	RequestTimeout   time.Duration      `yaml:"request_timeout"`
	StreamingTimeout time.Duration      `yaml:"streaming_timeout"`
	MaxRequestBodyMB int                `yaml:"max_request_body_mb"`
	SessionSecret    string             `yaml:"session_secret"`
	DefaultAdmin     DefaultAdminConfig `yaml:"default_admin"`
}

type CircuitBreakerConfig struct {
	Enabled          bool          `yaml:"enabled"`
	ErrorThreshold   float64       `yaml:"error_threshold"`
	MinRequests      int           `yaml:"min_requests"`
	CooldownDuration time.Duration `yaml:"cooldown_duration"`
}

type RetryConfig struct {
	InitialBackoff time.Duration `yaml:"initial_backoff"`
	MaxBackoff     time.Duration `yaml:"max_backoff"`
	Multiplier     float64       `yaml:"multiplier"`
}

type DebugConfig struct {
	Enabled       bool   `yaml:"enabled"`
	MaxBodyBytes  int    `yaml:"max_body_bytes"` // 0 = unlimited (save full bodies)
	RetentionDays int    `yaml:"retention_days"`
	DataDir       string `yaml:"data_dir"`
}

type CORSConfig struct {
	Enabled        bool     `yaml:"enabled"`
	AllowedOrigins []string `yaml:"allowed_origins"`
	AllowedMethods []string `yaml:"allowed_methods"`
	AllowedHeaders []string `yaml:"allowed_headers"`
	MaxAge         int      `yaml:"max_age"`
}

type DatabaseConfig struct {
	Path          string `yaml:"path"`
	RetentionDays int    `yaml:"retention_days"`
}

type CacheConfig struct {
	Enabled bool   `yaml:"enabled"`
	Address string `yaml:"address"`
	TTL     int    `yaml:"ttl"` // seconds
}

type ProviderConfig struct {
	Name     string        `yaml:"name"`
	BaseURL  string        `yaml:"base_url"`
	APIKey   string        `yaml:"api_key"`
	Priority int           `yaml:"priority"`
	Timeout  time.Duration `yaml:"timeout"`
}

type ModelConfig struct {
	Name             string        `yaml:"name"`
	Aliases          []string      `yaml:"aliases"`
	Routes           []RouteConfig `yaml:"routes"`
	LoadBalancing    string        `yaml:"load_balancing"`    // first, round-robin, random, least-cost
	RequestTimeout   time.Duration `yaml:"request_timeout"`   // per-model override; 0 = use server default
	StreamingTimeout time.Duration `yaml:"streaming_timeout"` // per-model override; 0 = use server default
}

type RouteConfig struct {
	Provider string        `yaml:"provider"`
	Model    string        `yaml:"model"`
	Pricing  PricingConfig `yaml:"pricing"`
}

type PricingConfig struct {
	Input  float64 `yaml:"input"`  // cost per 1M tokens
	Output float64 `yaml:"output"` // cost per 1M tokens
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	// Expand environment variables
	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validating config: %w", err)
	}

	return &cfg, nil
}

// Validate checks the config for correctness and applies defaults.
func (c *Config) Validate() error {
	if c.Server.Listen == "" {
		c.Server.Listen = "0.0.0.0:3000"
	}
	if c.Server.RequestTimeout == 0 {
		c.Server.RequestTimeout = 300 * time.Second
	}
	if c.Server.MaxRequestBodyMB == 0 {
		c.Server.MaxRequestBodyMB = 10
	}
	if c.Server.SessionSecret == "" {
		b := make([]byte, 32)
		rand.Read(b)
		c.Server.SessionSecret = hex.EncodeToString(b)
		log.Println("WARNING: no session_secret configured, generated random one (sessions won't survive restart)")
	}
	if c.Database.Path == "" {
		c.Database.Path = "janus.db"
	}
	if c.Database.RetentionDays == 0 {
		c.Database.RetentionDays = 90
	}
	if c.Pricing.RefreshInterval == 0 {
		c.Pricing.RefreshInterval = 6 * time.Hour
	}

	// Server defaults
	if c.Server.StreamingTimeout == 0 {
		c.Server.StreamingTimeout = 5 * time.Minute
	}

	// Circuit breaker defaults
	if c.CircuitBreaker.ErrorThreshold == 0 {
		c.CircuitBreaker.ErrorThreshold = 0.5
	}
	if c.CircuitBreaker.MinRequests == 0 {
		c.CircuitBreaker.MinRequests = 5
	}
	if c.CircuitBreaker.CooldownDuration == 0 {
		c.CircuitBreaker.CooldownDuration = 30 * time.Second
	}

	// Retry defaults
	if c.Retry.InitialBackoff == 0 {
		c.Retry.InitialBackoff = 100 * time.Millisecond
	}
	if c.Retry.MaxBackoff == 0 {
		c.Retry.MaxBackoff = 5 * time.Second
	}
	if c.Retry.Multiplier == 0 {
		c.Retry.Multiplier = 2.0
	}

	// Debug defaults
	if c.Debug.RetentionDays == 0 {
		c.Debug.RetentionDays = 90
	}

	// CORS defaults
	if c.CORS.MaxAge == 0 {
		c.CORS.MaxAge = 300
	}

	// Dedup defaults
	if c.Dedup.Window == 0 {
		c.Dedup.Window = 30 * time.Second
	}

	if len(c.Providers) == 0 {
		return fmt.Errorf("at least one provider is required")
	}
	providerNames := make(map[string]bool)
	for i, p := range c.Providers {
		if p.Name == "" || p.BaseURL == "" || p.APIKey == "" {
			return fmt.Errorf("provider %d: name, base_url, and api_key are required", i)
		}
		if providerNames[p.Name] {
			return fmt.Errorf("duplicate provider name: %s", p.Name)
		}
		providerNames[p.Name] = true
		if c.Providers[i].Timeout == 0 {
			c.Providers[i].Timeout = 120 * time.Second
		}
		if c.Providers[i].Priority == 0 {
			c.Providers[i].Priority = 1
		}
	}

	if len(c.Models) == 0 {
		return fmt.Errorf("at least one model is required")
	}
	modelNames := make(map[string]bool)
	for i, m := range c.Models {
		if m.Name == "" {
			return fmt.Errorf("model %d: name is required", i)
		}
		if modelNames[m.Name] {
			return fmt.Errorf("duplicate model name: %s", m.Name)
		}
		modelNames[m.Name] = true
		for _, alias := range m.Aliases {
			if modelNames[alias] {
				return fmt.Errorf("model alias %s conflicts with existing model or alias", alias)
			}
			modelNames[alias] = true
		}
		if len(m.Routes) == 0 {
			return fmt.Errorf("model %s: at least one route is required", m.Name)
		}
		for j, r := range m.Routes {
			if r.Provider == "" || r.Model == "" {
				return fmt.Errorf("model %s route %d: provider and model are required", m.Name, j)
			}
			if !providerNames[r.Provider] {
				return fmt.Errorf("model %s route %d: unknown provider %s", m.Name, j, r.Provider)
			}
		}
	}

	// Validate tokens (optional section)
	for i, t := range c.Tokens {
		if t.Key == "" {
			log.Printf("WARNING: token %d (%s) has empty key, skipping", i, t.Name)
			continue
		}
		if t.Name == "" {
			c.Tokens[i].Name = fmt.Sprintf("token-%d", i)
		}
	}

	return nil
}

// ValidateBytes parses raw YAML and returns a list of validation errors.
func ValidateBytes(data []byte) []string {
	expanded := os.ExpandEnv(string(data))
	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return []string{"parse error: " + err.Error()}
	}
	if err := cfg.Validate(); err != nil {
		return []string{err.Error()}
	}
	return nil
}

// ProviderByName returns the provider config by name.
func (c *Config) ProviderByName(name string) *ProviderConfig {
	for i := range c.Providers {
		if c.Providers[i].Name == name {
			return &c.Providers[i]
		}
	}
	return nil
}
