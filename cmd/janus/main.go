package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	gocors "github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"janus/internal/auth"
	"janus/internal/cache"
	"janus/internal/config"
	"janus/internal/dashboard"
	"janus/internal/metrics"
	"janus/internal/pricing"
	"janus/internal/provider"
	"janus/internal/proxy"
	"janus/internal/storage"
	"janus/internal/webhook"
)

var version = "dev"

func main() {
	configPath := flag.String("config", "configs/config.yaml", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("janus " + version)
		os.Exit(0)
	}

	log.Printf("janus %s starting", version)

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Pricing lookup (fetches from URL, refreshes periodically)
	pricingLookup := pricing.NewLookup(cfg.Pricing.URL, cfg.Pricing.RefreshInterval)
	defer pricingLookup.Close()

	// Auto-fill missing pricing from fetched data
	for i, m := range cfg.Models {
		for j, r := range m.Routes {
			if r.Pricing.Input == 0 && r.Pricing.Output == 0 {
				if pricingLookup.FillMissing(r.Provider, r.Model, &cfg.Models[i].Routes[j].Pricing.Input, &cfg.Models[i].Routes[j].Pricing.Output) {
					log.Printf("Auto-filled pricing for %s via %s: $%.2f/$%.2f per 1M tokens",
						m.Name, r.Provider, cfg.Models[i].Routes[j].Pricing.Input, cfg.Models[i].Routes[j].Pricing.Output)
				}
			}
		}
	}

	// Database
	db, err := storage.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	if err := db.CleanupOldRecords(cfg.Database.RetentionDays); err != nil {
		log.Printf("WARNING: retention cleanup failed: %v", err)
	}

	asyncLogger := storage.NewAsyncLogger(db, 1000)
	defer asyncLogger.Close()

	// SSE broker for real-time dashboard updates
	sseBroker := dashboard.NewSSEBroker()
	asyncLogger.OnFlush = sseBroker.Notify

	// Cache (optional)
	var c *cache.Cache
	if cfg.Cache.Enabled {
		c, err = cache.New(cfg.Cache.Address, cfg.Cache.TTL)
		if err != nil {
			log.Printf("WARNING: cache disabled: %v", err)
		} else {
			log.Printf("Cache connected to %s", cfg.Cache.Address)
		}
	}

	// Provider registry
	registry, err := provider.NewRegistry(cfg)
	if err != nil {
		log.Fatalf("Failed to build provider registry: %v", err)
	}
	log.Printf("Registered %d models", len(cfg.Models))

	// Provider health tracker
	healthTracker := provider.NewHealthTracker(5*time.Minute, cfg.CircuitBreaker)

	// Webhook notifier
	var notifier *webhook.Notifier
	if len(cfg.Webhooks) > 0 {
		notifier = webhook.NewNotifier(cfg.Webhooks)
		defer notifier.Close()
		log.Printf("Webhooks configured: %d endpoints", len(cfg.Webhooks))

		// Wire health tracker state changes to webhook
		healthTracker.OnStateChange = func(providerName string, from, to provider.CircuitState) {
			eventType := webhook.EventCircuitBreakerOpen
			if to == provider.CircuitClosed {
				eventType = webhook.EventCircuitBreakerClosed
			}
			notifier.Notify(webhook.Event{
				Type: eventType,
				Data: map[string]any{
					"provider": providerName,
					"from":     from.String(),
					"to":       to.String(),
				},
			})
		}
	}

	// Auth store (static tokens checked in-memory, not seeded to DB)
	var staticTokens []auth.StaticToken
	for _, t := range cfg.Tokens {
		if t.Key != "" {
			staticTokens = append(staticTokens, auth.StaticToken{
				Name:             t.Name,
				Key:              t.Key,
				RateLimitRPM:     t.RateLimitRPM,
				DailyBudgetUSD:   t.DailyBudgetUSD,
				MonthlyBudgetUSD: t.MonthlyBudgetUSD,
				MaxConcurrent:    t.MaxConcurrent,
			})
			log.Printf("Loaded static token: %s", t.Name)
		}
	}
	authStore := auth.NewStore(db.DB, staticTokens)
	authMiddleware := auth.NewMiddleware(authStore)
	authHandlers := auth.NewHandlers(authStore, cfg.Server.SessionSecret)

	// Audit logger
	auditLogger := storage.NewAuditLogger(db)
	auditLogger.OnWrite = sseBroker.Notify
	authHandlers.SetAuditLogger(auditLogger)

	// Debug logger
	debugDataDir := cfg.Debug.DataDir
	if debugDataDir == "" {
		debugDataDir = filepath.Dir(cfg.Database.Path)
	}
	debugLogger := storage.NewDebugLogger(db, cfg.Debug.Enabled, debugDataDir)
	debugLogger.OnWrite = sseBroker.Notify

	// Seed default admin
	seedDefaultAdmin(cfg, authStore)

	// Metrics
	m := metrics.New()

	// Handlers
	proxyHandler := proxy.NewHandler(registry, asyncLogger, c, m, cfg, healthTracker)
	proxyHandler.SetDebugLogger(debugLogger)

	// Request deduplication
	if cfg.Dedup.Enabled {
		dedup := proxy.NewDeduplicator(cfg.Dedup.Window)
		defer dedup.Close()
		proxyHandler.SetDeduplicator(dedup)
		log.Printf("Request deduplication enabled (window: %v)", cfg.Dedup.Window)
	}

	modelsHandler := proxy.NewModelsHandler(registry, healthTracker, cfg)
	proxyAuth := proxy.NewAuthMiddleware(authStore)
	rateLimiter := proxy.NewRateLimiter(db)
	if notifier != nil {
		rateLimiter.SetNotifier(notifier)
	}
	concurrencyLimiter := proxy.NewConcurrencyLimiter()
	statsAPI := dashboard.NewStatsAPI(db, authStore)
	statsAPI.SetHealthTracker(healthTracker)
	statsAPI.SetAuditLogger(auditLogger)
	statsAPI.SetDebugLogger(debugLogger)
	statsAPI.SetConfigPath(*configPath)
	if c != nil {
		statsAPI.SetCache(c)
	}
	dash := dashboard.NewDashboard(authStore, statsAPI)
	dash.SetRegistry(registry)
	dash.SetAuditLogger(auditLogger)
	dash.SetDebugLogger(debugLogger)
	if c != nil {
		dash.SetCache(c)
	}

	// Export handler
	exportHandler := dashboard.NewExportHandler(db, authStore)

	// Router
	r := chi.NewRouter()

	// CORS (before other middleware)
	if cfg.CORS.Enabled {
		r.Use(gocors.Handler(gocors.Options{
			AllowedOrigins:   cfg.CORS.AllowedOrigins,
			AllowedMethods:   cfg.CORS.AllowedMethods,
			AllowedHeaders:   cfg.CORS.AllowedHeaders,
			MaxAge:           cfg.CORS.MaxAge,
			AllowCredentials: true,
		}))
	}

	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RequestID)

	// Health & metrics (public)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Ping(); err != nil {
			http.Error(w, "database unhealthy", http.StatusServiceUnavailable)
			return
		}
		if c != nil {
			if err := c.Ping(r.Context()); err != nil {
				http.Error(w, "cache unhealthy", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})
	r.Handle("/metrics", promhttp.Handler())

	// OpenAI-compatible API (API token auth via Bearer header)
	r.Group(func(r chi.Router) {
		r.Use(proxyAuth.Authenticate)
		r.Use(rateLimiter.Check)
		r.Use(concurrencyLimiter.Check)
		r.Post("/v1/chat/completions", proxyHandler.ChatCompletions)
		r.Post("/v1/embeddings", proxyHandler.Embeddings)
		r.Get("/v1/models", modelsHandler.ListModels)
	})

	// Auth pages (public)
	r.Get("/login", dash.LoginPage)
	r.Get("/setup", dash.SetupPage)

	// Auth API endpoints (public)
	r.Post("/api/auth/login", authHandlers.Login)
	r.Post("/api/auth/setup", authHandlers.Setup)
	r.Post("/api/auth/login/totp", authHandlers.LoginTOTP)

	// Favicon (prevent 401 noise in browser console)
	r.Get("/favicon.ico", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	// Root redirect
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
	})

	// Authenticated pages and API
	r.Group(func(r chi.Router) {
		r.Use(authMiddleware.RequireAuth)

		// Dashboard pages (HTMX)
		r.Get("/dashboard", dash.DashboardPage)
		r.Get("/logs", dash.LogsPage)
		r.Get("/models", dash.ModelsPage)
		r.Get("/tokens", dash.TokensPage)
		r.Get("/settings", dash.SettingsPage)

		// Admin-only pages
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.RequireAdmin)
			r.Get("/users", dash.UsersPage)
			r.Get("/audit", dash.AuditPage)
			r.Get("/debug", dash.DebugPage)
		})

		// Auth API
		r.Post("/api/auth/logout", authHandlers.Logout)
		r.Get("/api/auth/me", authHandlers.Me)
		r.Put("/api/auth/me/password", authHandlers.ChangePassword)
		r.Put("/api/auth/me/username", authHandlers.ChangeUsername)
		r.Put("/api/auth/me/email", authHandlers.ChangeEmail)
		r.Post("/api/auth/totp/setup", authHandlers.TOTPSetup)
		r.Post("/api/auth/totp/verify", authHandlers.TOTPVerify)
		r.Delete("/api/auth/totp", authHandlers.TOTPDisable)

		// API token management
		r.Get("/api/tokens", authHandlers.ListTokens)
		r.Post("/api/tokens", authHandlers.CreateToken)
		r.Delete("/api/tokens/{id}", authHandlers.DeleteToken)

		// SSE events
		r.Get("/api/events", sseBroker.ServeHTTP)

		// Dashboard stats
		r.Get("/api/stats/summary", statsAPI.Summary)
		r.Get("/api/stats/models", statsAPI.Models)
		r.Get("/api/stats/providers", statsAPI.Providers)
		r.Get("/api/stats/tokens", statsAPI.Tokens)
		r.Get("/api/stats/timeseries", statsAPI.Timeseries)
		r.Get("/api/stats/logs", statsAPI.Logs)
		r.Get("/api/stats/latency", statsAPI.Latency)
		r.Get("/api/stats/cost-breakdown", statsAPI.CostBreakdown)
		r.Get("/api/stats/provider-health", statsAPI.ProviderHealthHandler)
		r.Get("/api/stats/cache", statsAPI.CacheStats)

		// Data export
		r.Get("/api/export/logs", exportHandler.ExportLogs)
		r.Get("/api/export/stats", exportHandler.ExportStats)

		// Admin-only: user management, audit, debug, config validation
		r.Group(func(r chi.Router) {
			r.Use(authMiddleware.RequireAdmin)
			r.Get("/api/auth/users", authHandlers.ListUsers)
			r.Post("/api/auth/users", authHandlers.CreateUser)
			r.Delete("/api/auth/users/{id}", authHandlers.DeleteUser)

			// Audit log
			r.Get("/api/stats/audit", statsAPI.AuditLogs)

			// Config validation
			r.Get("/api/config/validate", statsAPI.ValidateConfig)

			// Debug logging
			r.Post("/api/debug/toggle", statsAPI.DebugToggle)
			r.Get("/api/debug/status", statsAPI.DebugStatus)
			r.Get("/api/debug/logs", statsAPI.DebugLogs)
			r.Get("/api/debug/logs/{requestID}", statsAPI.DebugLogByRequestID)
		})
	})

	// Periodic session cleanup and debug log cleanup
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if err := authStore.CleanExpiredSessions(); err != nil {
				log.Printf("WARNING: session cleanup failed: %v", err)
			}
			if err := debugLogger.Cleanup(cfg.Debug.RetentionDays); err != nil {
				log.Printf("WARNING: debug log cleanup failed: %v", err)
			}
		}
	}()

	// Server
	srv := &http.Server{
		Addr:         cfg.Server.Listen,
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: cfg.Server.RequestTimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Config hot-reload via SIGHUP
	config.WatchReload(*configPath, func(newCfg *config.Config) {
		// Reload registry (models, providers, routes)
		if err := registry.Reload(newCfg); err != nil {
			log.Printf("ERROR: registry reload failed: %v", err)
			return
		}
		log.Printf("Reloaded %d models", len(newCfg.Models))

		// Reload pricing
		for i, m := range newCfg.Models {
			for j, rt := range m.Routes {
				if rt.Pricing.Input == 0 && rt.Pricing.Output == 0 {
					pricingLookup.FillMissing(rt.Provider, rt.Model,
						&newCfg.Models[i].Routes[j].Pricing.Input,
						&newCfg.Models[i].Routes[j].Pricing.Output)
				}
			}
		}

		// Reload static tokens
		var newStaticTokens []auth.StaticToken
		for _, t := range newCfg.Tokens {
			if t.Key != "" {
				newStaticTokens = append(newStaticTokens, auth.StaticToken{
					Name:             t.Name,
					Key:              t.Key,
					RateLimitRPM:     t.RateLimitRPM,
					DailyBudgetUSD:   t.DailyBudgetUSD,
					MonthlyBudgetUSD: t.MonthlyBudgetUSD,
					MaxConcurrent:    t.MaxConcurrent,
				})
			}
		}
		authStore.SetStaticTokens(newStaticTokens)

		// Update config pointer for retry/debug/etc
		cfg = newCfg
	})

	// Graceful shutdown
	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("Listening on %s", cfg.Server.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed: %v", err)
		}
	}()

	<-done
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	srv.Shutdown(ctx)

	log.Println("Stopped")
}

// seedDefaultAdmin creates the default admin user if no users exist.
func seedDefaultAdmin(cfg *config.Config, authStore *auth.Store) {
	if !authStore.HasAnyUser() {
		da := cfg.Server.DefaultAdmin
		if da.Username != "" && da.Password != "" {
			user, err := authStore.CreateUser(da.Username, da.Password, true)
			if err != nil {
				log.Printf("WARNING: failed to create default admin: %v", err)
			} else {
				log.Printf("Created default admin user: %s (id=%d)", user.Username, user.ID)
			}
		}
	}
}
