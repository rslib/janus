package provider

import (
	"context"
	"io"
	"testing"

	"janus/internal/config"
)

// mockProvider implements the Provider interface for testing.
type mockProvider struct {
	name string
}

func (m *mockProvider) Name() string { return m.name }

func (m *mockProvider) ChatCompletion(_ context.Context, _ string, _ *ChatRequest) (*ChatResponse, error) {
	return nil, nil
}

func (m *mockProvider) ChatCompletionStream(_ context.Context, _ string, _ *ChatRequest) (io.ReadCloser, error) {
	return nil, nil
}

func (m *mockProvider) Embedding(_ context.Context, _ string, _ *EmbeddingRequest) (*EmbeddingResponse, error) {
	return nil, nil
}

// newTestRegistry builds a Registry directly without going through config parsing.
func newTestRegistry(models []testModel) *Registry {
	r := &Registry{
		routes:    make(map[string][]Route),
		balancers: make(map[string]LoadBalancer),
		aliases:   make(map[string]string),
	}

	for _, m := range models {
		r.routes[m.name] = m.routes
		r.balancers[m.name] = &FirstBalancer{}
		r.order = append(r.order, m.name)
		for _, alias := range m.aliases {
			r.aliases[alias] = m.name
		}
	}

	return r
}

type testModel struct {
	name    string
	aliases []string
	routes  []Route
}

func TestRegistry_Lookup_Canonical(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name: "gpt-4",
			routes: []Route{
				{Provider: &mockProvider{name: "openai"}, ProviderModel: "gpt-4", Priority: 1},
			},
		},
	})

	routes, ok := reg.Lookup("gpt-4")
	if !ok {
		t.Fatal("expected Lookup to find gpt-4")
	}
	if len(routes) != 1 {
		t.Fatalf("expected 1 route, got %d", len(routes))
	}
	if routes[0].Provider.Name() != "openai" {
		t.Errorf("expected provider 'openai', got %q", routes[0].Provider.Name())
	}
}

func TestRegistry_Lookup_Alias(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name:    "gpt-4",
			aliases: []string{"gpt4", "gpt-4-latest"},
			routes: []Route{
				{Provider: &mockProvider{name: "openai"}, ProviderModel: "gpt-4", Priority: 1},
			},
		},
	})

	tests := []struct {
		name  string
		model string
		found bool
	}{
		{"canonical", "gpt-4", true},
		{"alias1", "gpt4", true},
		{"alias2", "gpt-4-latest", true},
		{"unknown", "gpt-5", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			routes, ok := reg.Lookup(tt.model)
			if ok != tt.found {
				t.Fatalf("Lookup(%q) found=%v, want %v", tt.model, ok, tt.found)
			}
			if tt.found && len(routes) != 1 {
				t.Fatalf("expected 1 route, got %d", len(routes))
			}
		})
	}
}

func TestRegistry_ModelNames_IncludesAliases(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name:    "gpt-4",
			aliases: []string{"gpt4"},
			routes: []Route{
				{Provider: &mockProvider{name: "openai"}, ProviderModel: "gpt-4", Priority: 1},
			},
		},
		{
			name: "claude-3",
			routes: []Route{
				{Provider: &mockProvider{name: "anthropic"}, ProviderModel: "claude-3", Priority: 1},
			},
		},
	})

	names := reg.ModelNames()

	want := map[string]bool{"gpt-4": true, "gpt4": true, "claude-3": true}
	got := make(map[string]bool)
	for _, n := range names {
		got[n] = true
	}

	for name := range want {
		if !got[name] {
			t.Errorf("expected %q in ModelNames, not found", name)
		}
	}

	if len(names) != len(want) {
		t.Errorf("expected %d names, got %d: %v", len(want), len(names), names)
	}
}

func TestRegistry_AllRoutes_ShowsAliases(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name:    "gpt-4",
			aliases: []string{"gpt4", "gpt-4-latest"},
			routes: []Route{
				{Provider: &mockProvider{name: "openai"}, ProviderModel: "gpt-4", Priority: 1},
				{Provider: &mockProvider{name: "azure"}, ProviderModel: "gpt-4", Priority: 2},
			},
		},
	})

	allRoutes := reg.AllRoutes()
	if len(allRoutes) != 1 {
		t.Fatalf("expected 1 model, got %d", len(allRoutes))
	}

	m := allRoutes[0]
	if m.Name != "gpt-4" {
		t.Errorf("expected name 'gpt-4', got %q", m.Name)
	}

	aliasSet := make(map[string]bool)
	for _, a := range m.Aliases {
		aliasSet[a] = true
	}
	if !aliasSet["gpt4"] || !aliasSet["gpt-4-latest"] {
		t.Errorf("expected aliases [gpt4, gpt-4-latest], got %v", m.Aliases)
	}

	if len(m.Routes) != 2 {
		t.Fatalf("expected 2 routes, got %d", len(m.Routes))
	}
	if m.Routes[0].ProviderName != "openai" {
		t.Errorf("expected first route provider 'openai', got %q", m.Routes[0].ProviderName)
	}
	if m.Routes[1].ProviderName != "azure" {
		t.Errorf("expected second route provider 'azure', got %q", m.Routes[1].ProviderName)
	}
}

func TestRegistry_AllRoutes_ConfigOrder(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name: "model-b",
			routes: []Route{
				{Provider: &mockProvider{name: "prov"}, ProviderModel: "b", Priority: 1},
			},
		},
		{
			name: "model-a",
			routes: []Route{
				{Provider: &mockProvider{name: "prov"}, ProviderModel: "a", Priority: 1},
			},
		},
	})

	allRoutes := reg.AllRoutes()
	if len(allRoutes) != 2 {
		t.Fatalf("expected 2 models, got %d", len(allRoutes))
	}
	if allRoutes[0].Name != "model-b" {
		t.Errorf("expected first model 'model-b', got %q", allRoutes[0].Name)
	}
	if allRoutes[1].Name != "model-a" {
		t.Errorf("expected second model 'model-a', got %q", allRoutes[1].Name)
	}
}

func TestRegistry_PrioritySorting(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name: "multi-provider",
			routes: []Route{
				{Provider: &mockProvider{name: "low-priority"}, ProviderModel: "m", Priority: 3},
				{Provider: &mockProvider{name: "high-priority"}, ProviderModel: "m", Priority: 1},
				{Provider: &mockProvider{name: "mid-priority"}, ProviderModel: "m", Priority: 2},
			},
		},
	})

	// Note: routes are stored as given (sorting happens during buildFromConfig).
	// For this test we verify AllRoutes returns them in stored order.
	allRoutes := reg.AllRoutes()
	if len(allRoutes) != 1 {
		t.Fatalf("expected 1 model, got %d", len(allRoutes))
	}

	routes := allRoutes[0].Routes
	if len(routes) != 3 {
		t.Fatalf("expected 3 routes, got %d", len(routes))
	}

	// Verify the priorities are present
	priorities := make(map[int]bool)
	for _, r := range routes {
		priorities[r.Priority] = true
	}
	for _, p := range []int{1, 2, 3} {
		if !priorities[p] {
			t.Errorf("expected priority %d in routes", p)
		}
	}
}

func TestRegistry_NewRegistry_UnknownProvider(t *testing.T) {
	cfg := &config.Config{
		Models: []config.ModelConfig{
			{
				Name: "test-model",
				Routes: []config.RouteConfig{
					{Provider: "nonexistent", Model: "m"},
				},
			},
		},
	}

	_, err := NewRegistry(cfg)
	if err == nil {
		t.Fatal("expected error for unknown provider, got nil")
	}
}

func TestRegistry_Lookup_NotFound(t *testing.T) {
	reg := newTestRegistry([]testModel{
		{
			name: "gpt-4",
			routes: []Route{
				{Provider: &mockProvider{name: "openai"}, ProviderModel: "gpt-4", Priority: 1},
			},
		},
	})

	_, ok := reg.Lookup("nonexistent")
	if ok {
		t.Fatal("expected Lookup to return false for nonexistent model")
	}
}
