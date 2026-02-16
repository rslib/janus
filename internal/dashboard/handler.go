package dashboard

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"janus/internal/auth"
	"janus/internal/cache"
	"janus/internal/provider"
	"janus/internal/storage"
)

//go:embed templates/*.html templates/partials/*.html
var templateFiles embed.FS

var templateFuncs = template.FuncMap{
	"formatTime": func(ts int64) string {
		if ts == 0 {
			return "never"
		}
		return time.Unix(ts, 0).Format("2006-01-02")
	},
	"formatTimeDetail": func(ts int64) string {
		if ts == 0 {
			return "never"
		}
		return time.Unix(ts, 0).Format("2006-01-02 15:04:05")
	},
	"addInt": func(a, b int) int {
		return a + b
	},
	"subInt": func(a, b int) int {
		return a - b
	},
	"formatCost": func(v float64) string {
		if v == 0 {
			return "$0.00"
		}
		if v < 0.01 {
			return fmt.Sprintf("$%.6f", v)
		}
		return fmt.Sprintf("$%.4f", v)
	},
	"formatPrice": func(v float64) string {
		if v == 0 {
			return "-"
		}
		return fmt.Sprintf("$%.2f", v)
	},
	"formatPct": func(v float64) string {
		return fmt.Sprintf("%.1f%%", v*100)
	},
	"budgetPct": func(spend, budget float64) float64 {
		if budget <= 0 {
			return 0
		}
		return spend / budget * 100
	},
	"budgetColor": func(pct float64) string {
		if pct >= 80 {
			return "#f87171"
		}
		if pct >= 50 {
			return "#fbbf24"
		}
		return "#4ade80"
	},
	"seq": func(start, end int) []int {
		var s []int
		for i := start; i <= end; i++ {
			s = append(s, i)
		}
		return s
	},
	"paginationStart": func(page, totalPages int) int {
		start := page - 2
		if start < 1 {
			start = 1
		}
		if totalPages-start < 4 && totalPages > 4 {
			start = totalPages - 4
		}
		return start
	},
	"paginationEnd": func(page, totalPages int) int {
		start := page - 2
		if start < 1 {
			start = 1
		}
		end := start + 4
		if end > totalPages {
			end = totalPages
		}
		return end
	},
}

// PageData is the common data passed to all templates.
type PageData struct {
	ActivePage string
	User       *auth.User
	// Dashboard data
	Summary        *SummaryResult
	Models         []ModelStats
	Providers      []ProviderStats
	TokenStats     []TokenUsageStats
	ProviderHealth []provider.ProviderHealth
	Latency        *LatencyResult
	CacheEnabled   bool
	CacheInfo      *cache.CacheStats
	// Tokens page data
	Tokens     []auth.APIToken
	TokenSpend map[string]float64
	// Users page data
	Users []auth.User
	// Logs page data
	LogsResult   *LogsResult
	LogModels    []string
	LogTokens    []string
	FilterModel  string
	FilterToken  string
	FilterStatus string
	// Models routing page data
	ModelRoutes []provider.ModelRouteInfo
	// Audit page data
	AuditResult        *storage.AuditQueryResult
	AuditFilterActions []string
	FilterAction       string
	// Debug page data
	DebugResult  *storage.DebugLogQueryResult
	DebugEnabled bool
}

// Dashboard serves the HTMX-based dashboard pages.
type Dashboard struct {
	templates   *template.Template
	authStore   *auth.Store
	statsAPI    *StatsAPI
	registry    *provider.Registry
	cache       *cache.Cache
	auditLogger *storage.AuditLogger
	debugLogger *storage.DebugLogger
}

// NewDashboard creates a new Dashboard handler.
func NewDashboard(authStore *auth.Store, statsAPI *StatsAPI) *Dashboard {
	tmpl := template.Must(
		template.New("").Funcs(templateFuncs).ParseFS(templateFiles,
			"templates/*.html",
			"templates/partials/*.html",
		),
	)

	return &Dashboard{
		templates: tmpl,
		authStore: authStore,
		statsAPI:  statsAPI,
	}
}

// SetRegistry sets the provider registry for model routing display.
func (d *Dashboard) SetRegistry(r *provider.Registry) {
	d.registry = r
}

// SetCache sets the cache reference for cache stats display.
func (d *Dashboard) SetCache(c *cache.Cache) {
	d.cache = c
}

// SetAuditLogger sets the audit logger for the audit page.
func (d *Dashboard) SetAuditLogger(al *storage.AuditLogger) {
	d.auditLogger = al
}

// SetDebugLogger sets the debug logger for the debug page.
func (d *Dashboard) SetDebugLogger(dl *storage.DebugLogger) {
	d.debugLogger = dl
}

// LoginPage serves the login page.
func (d *Dashboard) LoginPage(w http.ResponseWriter, r *http.Request) {
	if !d.authStore.HasAnyUser() {
		http.Redirect(w, r, "/setup", http.StatusFound)
		return
	}
	if user := d.getSessionUser(r); user != nil {
		http.Redirect(w, r, "/dashboard", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.templates.ExecuteTemplate(w, "login", nil)
}

// SetupPage serves the initial setup page.
func (d *Dashboard) SetupPage(w http.ResponseWriter, r *http.Request) {
	if d.authStore.HasAnyUser() {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	d.templates.ExecuteTemplate(w, "setup", nil)
}

// DashboardPage serves the main dashboard view.
func (d *Dashboard) DashboardPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	tokenNames := d.statsAPI.TokenNamesForUser(user)

	data := PageData{
		ActivePage: "dashboard",
		User:       user,
		Summary:    d.statsAPI.GetSummary(tokenNames),
		Models:     d.statsAPI.GetModels(tokenNames),
		Providers:  d.statsAPI.GetProviders(tokenNames),
		TokenStats: d.statsAPI.GetTokenUsage(tokenNames),
		Latency:    d.statsAPI.GetLatency(tokenNames, "24h", "", ""),
	}

	// Provider health
	if d.statsAPI.healthTracker != nil {
		data.ProviderHealth = d.statsAPI.healthTracker.Status()
	}

	// Cache stats
	if d.cache != nil {
		data.CacheEnabled = true
		data.CacheInfo = d.cache.Stats(r.Context())
	}

	d.renderDashboardPage(w, r, "partials/dashboard.html", data)
}

// LogsPage serves the request logs view.
func (d *Dashboard) LogsPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	tokenNames := d.statsAPI.TokenNamesForUser(user)

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	model := r.URL.Query().Get("model")
	token := r.URL.Query().Get("token")
	status := r.URL.Query().Get("status")

	data := PageData{
		ActivePage:   "logs",
		User:         user,
		LogsResult:   d.statsAPI.GetLogs(tokenNames, page, model, token, status),
		LogModels:    d.statsAPI.GetDistinctModels(),
		LogTokens:    d.statsAPI.GetDistinctTokens(),
		FilterModel:  model,
		FilterToken:  token,
		FilterStatus: status,
	}

	d.renderDashboardPage(w, r, "partials/logs.html", data)
}

// ModelsPage serves the model routing table view.
func (d *Dashboard) ModelsPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())

	data := PageData{
		ActivePage: "models",
		User:       user,
	}

	if d.registry != nil {
		data.ModelRoutes = d.registry.AllRoutes()
	}

	if d.statsAPI.healthTracker != nil {
		data.ProviderHealth = d.statsAPI.healthTracker.Status()
	}

	d.renderDashboardPage(w, r, "partials/models-page.html", data)
}

// TokensPage serves the tokens management view.
func (d *Dashboard) TokensPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())

	var userID int64
	if !user.IsAdmin {
		userID = user.ID
	}

	tokens, _ := d.authStore.ListAPITokens(userID)
	if tokens == nil {
		tokens = []auth.APIToken{}
	}

	// Get today's spend for budget display
	spend, _ := d.statsAPI.db.TodaySpendAll()
	if spend == nil {
		spend = make(map[string]float64)
	}

	d.renderDashboardPage(w, r, "partials/tokens.html", PageData{
		ActivePage: "tokens",
		User:       user,
		Tokens:     tokens,
		TokenSpend: spend,
	})
}

// UsersPage serves the user management view (admin only).
func (d *Dashboard) UsersPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	users, _ := d.authStore.ListUsers()

	d.renderDashboardPage(w, r, "partials/users.html", PageData{
		ActivePage: "users",
		User:       user,
		Users:      users,
	})
}

// AuditPage serves the audit log view (admin only).
func (d *Dashboard) AuditPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	action := r.URL.Query().Get("action")
	since := time.Now().AddDate(0, 0, -30).Unix()

	var auditResult *storage.AuditQueryResult
	if d.auditLogger != nil {
		auditResult = d.auditLogger.Query(since, action, page, 50)
	} else {
		auditResult = &storage.AuditQueryResult{Entries: []storage.AuditEntry{}, Page: 1, TotalPages: 1}
	}

	// Common audit action types for the filter dropdown
	actions := []string{"login", "logout", "create_user", "delete_user", "create_token", "delete_token", "change_password", "setup_totp", "disable_totp"}

	d.renderDashboardPage(w, r, "partials/audit.html", PageData{
		ActivePage:         "audit",
		User:               user,
		AuditResult:        auditResult,
		AuditFilterActions: actions,
		FilterAction:       action,
	})
}

// DebugPage serves the debug logging view (admin only).
func (d *Dashboard) DebugPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	var debugResult *storage.DebugLogQueryResult
	debugEnabled := false
	if d.debugLogger != nil {
		debugResult = d.debugLogger.QueryFull(page, 50)
		debugEnabled = d.debugLogger.IsEnabled()
	} else {
		debugResult = &storage.DebugLogQueryResult{Entries: []storage.DebugLogEntry{}, Page: 1, TotalPages: 1}
	}

	d.renderDashboardPage(w, r, "partials/debug.html", PageData{
		ActivePage:   "debug",
		User:         user,
		DebugResult:  debugResult,
		DebugEnabled: debugEnabled,
	})
}

// SettingsPage serves the settings view.
func (d *Dashboard) SettingsPage(w http.ResponseWriter, r *http.Request) {
	user := auth.UserFromContext(r.Context())
	user, _ = d.authStore.GetUserByID(user.ID)

	d.renderDashboardPage(w, r, "partials/settings.html", PageData{
		ActivePage: "settings",
		User:       user,
	})
}

// renderDashboardPage renders either the full layout or just the content partial.
func (d *Dashboard) renderDashboardPage(w http.ResponseWriter, r *http.Request, partialFile string, data PageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if r.Header.Get("HX-Request") == "true" {
		tmpl := template.Must(
			template.New("").Funcs(templateFuncs).ParseFS(templateFiles, "templates/"+partialFile),
		)
		tmpl.ExecuteTemplate(w, "content", data)
	} else {
		tmpl := template.Must(
			template.New("").Funcs(templateFuncs).ParseFS(templateFiles,
				"templates/layout.html",
				"templates/"+partialFile,
			),
		)
		tmpl.ExecuteTemplate(w, "layout", data)
	}
}

func (d *Dashboard) getSessionUser(r *http.Request) *auth.User {
	cookie, err := r.Cookie("llmgw_session")
	if err != nil || cookie.Value == "" {
		return nil
	}
	sess, err := d.authStore.GetSession(cookie.Value)
	if err != nil {
		return nil
	}
	user, err := d.authStore.GetUserByID(sess.UserID)
	if err != nil {
		return nil
	}
	return user
}
