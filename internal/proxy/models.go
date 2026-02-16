package proxy

import (
	"encoding/json"
	"net/http"
	"time"

	"janus/internal/config"
	"janus/internal/provider"
)

type ModelsHandler struct {
	registry      *provider.Registry
	healthTracker *provider.HealthTracker
	cfg           *config.Config
}

func NewModelsHandler(registry *provider.Registry, healthTracker *provider.HealthTracker, cfg *config.Config) *ModelsHandler {
	return &ModelsHandler{
		registry:      registry,
		healthTracker: healthTracker,
		cfg:           cfg,
	}
}

func (h *ModelsHandler) ListModels(w http.ResponseWriter, r *http.Request) {
	allRoutes := h.registry.AllRoutes()
	models := make([]map[string]any, 0, len(allRoutes))

	for _, m := range allRoutes {
		providers := make([]map[string]any, 0, len(m.Routes))
		for _, rt := range m.Routes {
			healthy := true
			if h.healthTracker != nil {
				healthy = h.healthTracker.IsAvailable(rt.ProviderName)
			}
			providers = append(providers, map[string]any{
				"name":         rt.ProviderName,
				"model":        rt.ProviderModel,
				"input_price":  rt.InputPrice,
				"output_price": rt.OutputPrice,
				"priority":     rt.Priority,
				"healthy":      healthy,
			})
		}

		// Find load balancing strategy from config
		loadBalancing := "first"
		for _, mc := range h.cfg.Models {
			if mc.Name == m.Name {
				if mc.LoadBalancing != "" {
					loadBalancing = mc.LoadBalancing
				}
				break
			}
		}

		models = append(models, map[string]any{
			"id":             m.Name,
			"object":         "model",
			"created":        time.Now().Unix(),
			"owned_by":       "janus",
			"providers":      providers,
			"provider_count": len(providers),
			"load_balancing": loadBalancing,
			"aliases":        m.Aliases,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   models,
	})
}
