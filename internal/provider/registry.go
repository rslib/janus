package provider

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"janus/internal/config"
)

// ModelTimeouts holds per-model timeout overrides.
type ModelTimeouts struct {
	RequestTimeout   time.Duration
	StreamingTimeout time.Duration
}

// Route maps a model to a specific provider with pricing.
type Route struct {
	Provider      Provider
	ProviderModel string
	Priority      int
	InputPrice    float64 // per 1M tokens
	OutputPrice   float64 // per 1M tokens
}

// Registry maps model names to provider routes.
type Registry struct {
	mu        sync.RWMutex
	routes    map[string][]Route
	balancers map[string]LoadBalancer
	aliases   map[string]string // alias -> canonical name
	order     []string          // preserves config order (canonical names only)
	timeouts  map[string]*ModelTimeouts
}

func NewRegistry(cfg *config.Config) (*Registry, error) {
	r := &Registry{}
	if err := r.buildFromConfig(cfg); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) buildFromConfig(cfg *config.Config) error {
	// Build providers
	providers := make(map[string]Provider)
	for _, pc := range cfg.Providers {
		providers[pc.Name] = NewOpenAIProvider(pc.Name, pc.BaseURL, pc.APIKey, pc.Timeout)
	}

	// Build routes
	routes := make(map[string][]Route)
	balancers := make(map[string]LoadBalancer)
	aliases := make(map[string]string)
	order := make([]string, 0, len(cfg.Models))
	timeouts := make(map[string]*ModelTimeouts)

	for _, mc := range cfg.Models {
		var modelRoutes []Route
		for _, rc := range mc.Routes {
			p, ok := providers[rc.Provider]
			if !ok {
				return fmt.Errorf("model %s: unknown provider %s", mc.Name, rc.Provider)
			}
			pc := cfg.ProviderByName(rc.Provider)
			priority := pc.Priority
			modelRoutes = append(modelRoutes, Route{
				Provider:      p,
				ProviderModel: rc.Model,
				Priority:      priority,
				InputPrice:    rc.Pricing.Input,
				OutputPrice:   rc.Pricing.Output,
			})
		}
		// Sort by priority (lower = higher priority)
		sort.Slice(modelRoutes, func(i, j int) bool {
			return modelRoutes[i].Priority < modelRoutes[j].Priority
		})
		routes[mc.Name] = modelRoutes
		order = append(order, mc.Name)

		// Load balancer
		balancers[mc.Name] = NewLoadBalancer(mc.LoadBalancing)

		// Per-model timeouts
		if mc.RequestTimeout > 0 || mc.StreamingTimeout > 0 {
			timeouts[mc.Name] = &ModelTimeouts{
				RequestTimeout:   mc.RequestTimeout,
				StreamingTimeout: mc.StreamingTimeout,
			}
		}

		// Register aliases
		for _, alias := range mc.Aliases {
			aliases[alias] = mc.Name
		}
	}

	r.mu.Lock()
	r.routes = routes
	r.balancers = balancers
	r.aliases = aliases
	r.order = order
	r.timeouts = timeouts
	r.mu.Unlock()

	return nil
}

// Reload rebuilds routes from new config. Used for hot-reload.
func (r *Registry) Reload(cfg *config.Config) error {
	return r.buildFromConfig(cfg)
}

// Lookup returns the routes for a model name (resolving aliases).
func (r *Registry) Lookup(model string) ([]Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Resolve alias
	canonical := model
	if alias, ok := r.aliases[model]; ok {
		canonical = alias
	}

	routes, ok := r.routes[canonical]
	if !ok {
		return nil, false
	}

	// Apply load balancer
	if balancer, ok := r.balancers[canonical]; ok {
		routes = balancer.Reorder(routes)
	}

	return routes, true
}

// ModelNames returns all registered model names in config order (including aliases).
func (r *Registry) ModelNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var names []string
	for _, name := range r.order {
		names = append(names, name)
	}
	// Add aliases
	for alias := range r.aliases {
		names = append(names, alias)
	}
	return names
}

// ModelTimeoutsFor returns per-model timeout overrides, resolving aliases. Returns nil if none set.
func (r *Registry) ModelTimeoutsFor(model string) *ModelTimeouts {
	r.mu.RLock()
	defer r.mu.RUnlock()

	canonical := model
	if alias, ok := r.aliases[model]; ok {
		canonical = alias
	}
	return r.timeouts[canonical]
}

// RouteInfo exposes route details for dashboard display.
type RouteInfo struct {
	ProviderName  string  `json:"provider_name"`
	ProviderModel string  `json:"provider_model"`
	Priority      int     `json:"priority"`
	InputPrice    float64 `json:"input_price"`
	OutputPrice   float64 `json:"output_price"`
}

// ModelRouteInfo exposes a model and its routes for dashboard display.
type ModelRouteInfo struct {
	Name    string      `json:"name"`
	Aliases []string    `json:"aliases,omitempty"`
	Routes  []RouteInfo `json:"routes"`
}

// AllRoutes returns all models and their routes in config order.
func (r *Registry) AllRoutes() []ModelRouteInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Build reverse alias map
	modelAliases := make(map[string][]string)
	for alias, canonical := range r.aliases {
		modelAliases[canonical] = append(modelAliases[canonical], alias)
	}

	results := make([]ModelRouteInfo, 0, len(r.order))
	for _, name := range r.order {
		routes := r.routes[name]
		info := ModelRouteInfo{
			Name:    name,
			Aliases: modelAliases[name],
		}
		for _, rt := range routes {
			info.Routes = append(info.Routes, RouteInfo{
				ProviderName:  rt.Provider.Name(),
				ProviderModel: rt.ProviderModel,
				Priority:      rt.Priority,
				InputPrice:    rt.InputPrice,
				OutputPrice:   rt.OutputPrice,
			})
		}
		results = append(results, info)
	}
	return results
}
