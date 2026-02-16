package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"janus/internal/auth"
	"janus/internal/cache"
	"janus/internal/config"
	"janus/internal/provider"
	"janus/internal/storage"
)

// Exported types for template rendering and JSON API.

type Period struct {
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	Errors       int     `json:"errors"`
	CachedHits   int     `json:"cached_hits"`
}

type SummaryResult struct {
	Today *Period `json:"today"`
	Week  *Period `json:"week"`
	Month *Period `json:"month"`
}

type ModelStats struct {
	Model        string  `json:"model"`
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

type ProviderStats struct {
	Provider     string  `json:"provider"`
	Requests     int     `json:"requests"`
	Successes    int     `json:"successes"`
	Errors       int     `json:"errors"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
	CostUSD      float64 `json:"cost_usd"`
}

type TokenUsageStats struct {
	TokenName    string  `json:"token_name"`
	Requests     int     `json:"requests"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// RequestLogEntry represents a single request log row.
type RequestLogEntry struct {
	RequestID     string  `json:"request_id"`
	Timestamp     int64   `json:"timestamp"`
	TokenName     string  `json:"token_name"`
	Model         string  `json:"model"`
	Provider      string  `json:"provider"`
	ProviderModel string  `json:"provider_model"`
	InputTokens   int     `json:"input_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	LatencyMS     int64   `json:"latency_ms"`
	Status        string  `json:"status"`
	ErrorMessage  string  `json:"error_message"`
	Streaming     bool    `json:"streaming"`
	Cached        bool    `json:"cached"`
}

// LogsResult holds paginated logs.
type LogsResult struct {
	Logs       []RequestLogEntry `json:"logs"`
	Page       int               `json:"page"`
	TotalPages int               `json:"total_pages"`
	Total      int               `json:"total"`
}

// LatencyResult holds latency percentiles.
type LatencyResult struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Avg float64 `json:"avg"`
	Min float64 `json:"min"`
	Max float64 `json:"max"`
}

// CostBreakdownEntry holds cost data grouped by day and dimension.
type CostBreakdownEntry struct {
	Day      string  `json:"day"`
	GroupBy  string  `json:"group_by"`
	CostUSD  float64 `json:"cost_usd"`
	Requests int     `json:"requests"`
}

type StatsAPI struct {
	db            *storage.DB
	authStore     *auth.Store
	healthTracker *provider.HealthTracker
	cache         *cache.Cache
	auditLogger   *storage.AuditLogger
	debugLogger   *storage.DebugLogger
	configPath    string
}

func NewStatsAPI(db *storage.DB, authStore *auth.Store) *StatsAPI {
	return &StatsAPI{db: db, authStore: authStore}
}

// SetHealthTracker sets the provider health tracker.
func (s *StatsAPI) SetHealthTracker(ht *provider.HealthTracker) {
	s.healthTracker = ht
}

// SetCache sets the cache for stats.
func (s *StatsAPI) SetCache(c *cache.Cache) {
	s.cache = c
}

// SetAuditLogger sets the audit logger.
func (s *StatsAPI) SetAuditLogger(al *storage.AuditLogger) {
	s.auditLogger = al
}

// SetDebugLogger sets the debug logger.
func (s *StatsAPI) SetDebugLogger(dl *storage.DebugLogger) {
	s.debugLogger = dl
}

// SetConfigPath sets the config file path for validation.
func (s *StatsAPI) SetConfigPath(path string) {
	s.configPath = path
}

// TokenNamesForUser returns the token names that belong to the user.
// Admins get nil (no filter), non-admins get their token names.
func (s *StatsAPI) TokenNamesForUser(user *auth.User) []string {
	if user == nil || user.IsAdmin {
		return nil
	}
	tokens, err := s.authStore.ListAPITokens(user.ID)
	if err != nil {
		return []string{"__none__"}
	}
	names := make([]string, len(tokens))
	for i, t := range tokens {
		names[i] = t.Name
	}
	if len(names) == 0 {
		return []string{"__none__"}
	}
	return names
}

// tokenNamesForUser returns token names from request context (for HTTP handlers).
func (s *StatsAPI) tokenNamesForUser(r *http.Request) []string {
	user := auth.UserFromContext(r.Context())
	return s.TokenNamesForUser(user)
}

func buildTokenFilter(tokenNames []string) (string, []any) {
	if tokenNames == nil {
		return "", nil
	}
	placeholders := ""
	args := make([]any, len(tokenNames))
	for i, n := range tokenNames {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
		args[i] = n
	}
	return " AND token_name IN (" + placeholders + ")", args
}

// Data-fetching methods (used by both JSON handlers and template handlers).

func (s *StatsAPI) GetSummary(tokenNames []string) *SummaryResult {
	now := time.Now()
	todayStart := now.Truncate(24 * time.Hour).Unix()
	weekStart := now.AddDate(0, 0, -7).Unix()
	monthStart := now.AddDate(0, -1, 0).Unix()

	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	result := &SummaryResult{
		Today: &Period{},
		Week:  &Period{},
		Month: &Period{},
	}

	periods := map[string]struct {
		since  int64
		period *Period
	}{
		"today": {todayStart, result.Today},
		"week":  {weekStart, result.Week},
		"month": {monthStart, result.Month},
	}

	for _, p := range periods {
		args := append([]any{p.since}, filterArgs...)
		row := s.db.QueryRow(`SELECT
			COUNT(*),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cost_usd), 0),
			COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN cached = 1 THEN 1 ELSE 0 END), 0)
			FROM request_logs WHERE timestamp >= ?`+tokenFilter, args...)
		row.Scan(&p.period.Requests, &p.period.InputTokens, &p.period.OutputTokens, &p.period.CostUSD, &p.period.Errors, &p.period.CachedHits)
	}

	return result
}

func (s *StatsAPI) GetModels(tokenNames []string) []ModelStats {
	since := time.Now().AddDate(0, 0, -30).Unix()
	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	args := append([]any{since}, filterArgs...)
	rows, err := s.db.Query(`SELECT
		model,
		COUNT(*) as requests,
		COALESCE(SUM(input_tokens), 0) as input_tokens,
		COALESCE(SUM(output_tokens), 0) as output_tokens,
		COALESCE(SUM(cost_usd), 0) as cost,
		COALESCE(AVG(latency_ms), 0) as avg_latency
		FROM request_logs WHERE timestamp >= ?`+tokenFilter+`
		GROUP BY model ORDER BY requests DESC`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []ModelStats
	for rows.Next() {
		var m ModelStats
		rows.Scan(&m.Model, &m.Requests, &m.InputTokens, &m.OutputTokens, &m.CostUSD, &m.AvgLatencyMS)
		results = append(results, m)
	}
	return results
}

func (s *StatsAPI) GetProviders(tokenNames []string) []ProviderStats {
	since := time.Now().AddDate(0, 0, -30).Unix()
	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	args := append([]any{since}, filterArgs...)
	rows, err := s.db.Query(`SELECT
		provider,
		COUNT(*) as requests,
		COALESCE(SUM(CASE WHEN status = 'success' THEN 1 ELSE 0 END), 0) as successes,
		COALESCE(SUM(CASE WHEN status = 'error' THEN 1 ELSE 0 END), 0) as errors,
		COALESCE(AVG(latency_ms), 0) as avg_latency,
		COALESCE(SUM(cost_usd), 0) as cost
		FROM request_logs WHERE timestamp >= ?`+tokenFilter+`
		GROUP BY provider ORDER BY requests DESC`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []ProviderStats
	for rows.Next() {
		var p ProviderStats
		rows.Scan(&p.Provider, &p.Requests, &p.Successes, &p.Errors, &p.AvgLatencyMS, &p.CostUSD)
		results = append(results, p)
	}
	return results
}

func (s *StatsAPI) GetTokenUsage(tokenNames []string) []TokenUsageStats {
	since := time.Now().AddDate(0, 0, -30).Unix()
	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	args := append([]any{since}, filterArgs...)
	rows, err := s.db.Query(`SELECT
		token_name,
		COUNT(*) as requests,
		COALESCE(SUM(input_tokens), 0) as input_tokens,
		COALESCE(SUM(output_tokens), 0) as output_tokens,
		COALESCE(SUM(cost_usd), 0) as cost
		FROM request_logs WHERE timestamp >= ?`+tokenFilter+`
		GROUP BY token_name ORDER BY requests DESC`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []TokenUsageStats
	for rows.Next() {
		var t TokenUsageStats
		rows.Scan(&t.TokenName, &t.Requests, &t.InputTokens, &t.OutputTokens, &t.CostUSD)
		results = append(results, t)
	}
	return results
}

// GetLogs returns paginated request logs with filters.
func (s *StatsAPI) GetLogs(tokenNames []string, page int, model, token, status string) *LogsResult {
	if page < 1 {
		page = 1
	}
	limit := 50
	offset := (page - 1) * limit

	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	where := "WHERE 1=1" + tokenFilter
	args := make([]any, 0)
	args = append(args, filterArgs...)

	if model != "" {
		where += " AND model = ?"
		args = append(args, model)
	}
	if token != "" {
		where += " AND token_name = ?"
		args = append(args, token)
	}
	if status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}

	// Get total count
	var total int
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	s.db.QueryRow("SELECT COUNT(*) FROM request_logs "+where, countArgs...).Scan(&total)

	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}

	// Get page
	query := `SELECT COALESCE(request_id, ''), timestamp, token_name, model, provider, provider_model,
		input_tokens, output_tokens, cost_usd, latency_ms, status,
		COALESCE(error_message, ''), streaming, cached
		FROM request_logs ` + where + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return &LogsResult{Logs: []RequestLogEntry{}, Page: page, TotalPages: totalPages, Total: total}
	}
	defer rows.Close()

	var logs []RequestLogEntry
	for rows.Next() {
		var l RequestLogEntry
		var streaming, cached int
		rows.Scan(&l.RequestID, &l.Timestamp, &l.TokenName, &l.Model, &l.Provider, &l.ProviderModel,
			&l.InputTokens, &l.OutputTokens, &l.CostUSD, &l.LatencyMS, &l.Status,
			&l.ErrorMessage, &streaming, &cached)
		l.Streaming = streaming == 1
		l.Cached = cached == 1
		logs = append(logs, l)
	}
	if logs == nil {
		logs = []RequestLogEntry{}
	}

	return &LogsResult{
		Logs:       logs,
		Page:       page,
		TotalPages: totalPages,
		Total:      total,
	}
}

// GetDistinctModels returns distinct model names from logs.
func (s *StatsAPI) GetDistinctModels() []string {
	rows, err := s.db.Query("SELECT DISTINCT model FROM request_logs ORDER BY model")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var models []string
	for rows.Next() {
		var m string
		rows.Scan(&m)
		models = append(models, m)
	}
	return models
}

// GetDistinctTokens returns distinct token names from logs.
func (s *StatsAPI) GetDistinctTokens() []string {
	rows, err := s.db.Query("SELECT DISTINCT token_name FROM request_logs ORDER BY token_name")
	if err != nil {
		return nil
	}
	defer rows.Close()
	var tokens []string
	for rows.Next() {
		var t string
		rows.Scan(&t)
		tokens = append(tokens, t)
	}
	return tokens
}

// GetLatency computes latency percentiles from request_logs.
func (s *StatsAPI) GetLatency(tokenNames []string, period, model, providerName string) *LatencyResult {
	var since int64
	switch period {
	case "7d":
		since = time.Now().AddDate(0, 0, -7).Unix()
	case "30d":
		since = time.Now().AddDate(0, -1, 0).Unix()
	default:
		since = time.Now().Add(-24 * time.Hour).Unix()
	}

	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	where := "WHERE timestamp >= ? AND status = 'success'" + tokenFilter
	args := []any{since}
	args = append(args, filterArgs...)

	if model != "" {
		where += " AND model = ?"
		args = append(args, model)
	}
	if providerName != "" {
		where += " AND provider = ?"
		args = append(args, providerName)
	}

	rows, err := s.db.Query("SELECT latency_ms FROM request_logs "+where+" ORDER BY latency_ms", args...)
	if err != nil {
		return &LatencyResult{}
	}
	defer rows.Close()

	var latencies []float64
	for rows.Next() {
		var l float64
		rows.Scan(&l)
		latencies = append(latencies, l)
	}

	if len(latencies) == 0 {
		return &LatencyResult{}
	}

	sort.Float64s(latencies)
	n := len(latencies)
	var sum float64
	for _, l := range latencies {
		sum += l
	}

	return &LatencyResult{
		P50: latencies[n*50/100],
		P95: latencies[n*95/100],
		P99: latencies[min(n*99/100, n-1)],
		Avg: sum / float64(n),
		Min: latencies[0],
		Max: latencies[n-1],
	}
}

// GetCostBreakdown returns cost data grouped by day and dimension.
func (s *StatsAPI) GetCostBreakdown(tokenNames []string, period, groupBy string) []CostBreakdownEntry {
	var since int64
	switch period {
	case "30d":
		since = time.Now().AddDate(0, -1, 0).Unix()
	case "7d":
		since = time.Now().AddDate(0, 0, -7).Unix()
	default:
		since = time.Now().Add(-24 * time.Hour).Unix()
	}

	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	groupCol := "model"
	if groupBy == "token" {
		groupCol = "token_name"
	} else if groupBy == "provider" {
		groupCol = "provider"
	}

	args := []any{since}
	args = append(args, filterArgs...)

	query := `SELECT date(timestamp, 'unixepoch') as day, ` + groupCol + `,
		COALESCE(SUM(cost_usd), 0), COUNT(*)
		FROM request_logs WHERE timestamp >= ?` + tokenFilter + `
		GROUP BY day, ` + groupCol + ` ORDER BY day, ` + groupCol

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var results []CostBreakdownEntry
	for rows.Next() {
		var e CostBreakdownEntry
		rows.Scan(&e.Day, &e.GroupBy, &e.CostUSD, &e.Requests)
		results = append(results, e)
	}
	return results
}

// JSON HTTP handlers (thin wrappers).

func (s *StatsAPI) Summary(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	result := s.GetSummary(tokenNames)
	writeJSON(w, result)
}

func (s *StatsAPI) Models(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	results := s.GetModels(tokenNames)
	writeJSON(w, results)
}

func (s *StatsAPI) Providers(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	results := s.GetProviders(tokenNames)
	writeJSON(w, results)
}

func (s *StatsAPI) Tokens(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	results := s.GetTokenUsage(tokenNames)
	writeJSON(w, results)
}

func (s *StatsAPI) Timeseries(w http.ResponseWriter, r *http.Request) {
	period := r.URL.Query().Get("period")
	var since int64
	var groupFmt string
	switch period {
	case "7d":
		since = time.Now().AddDate(0, 0, -7).Unix()
		groupFmt = "%Y-%m-%d"
	case "30d":
		since = time.Now().AddDate(0, -1, 0).Unix()
		groupFmt = "%Y-%m-%d"
	default:
		since = time.Now().Add(-24 * time.Hour).Unix()
		groupFmt = "%Y-%m-%d %H:00"
	}

	tokenNames := s.tokenNamesForUser(r)
	tokenFilter, filterArgs := buildTokenFilter(tokenNames)

	args := append([]any{since}, filterArgs...)
	rows, err := s.db.Query(`SELECT
		strftime('`+groupFmt+`', timestamp, 'unixepoch') as bucket,
		COUNT(*) as requests,
		COALESCE(SUM(cost_usd), 0) as cost,
		COALESCE(SUM(input_tokens + output_tokens), 0) as total_tokens
		FROM request_logs WHERE timestamp >= ?`+tokenFilter+`
		GROUP BY bucket ORDER BY bucket`, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type point struct {
		Bucket      string  `json:"bucket"`
		Requests    int     `json:"requests"`
		CostUSD     float64 `json:"cost_usd"`
		TotalTokens int     `json:"total_tokens"`
	}

	var results []point
	for rows.Next() {
		var p point
		rows.Scan(&p.Bucket, &p.Requests, &p.CostUSD, &p.TotalTokens)
		results = append(results, p)
	}
	writeJSON(w, results)
}

// Logs serves the paginated logs API.
func (s *StatsAPI) Logs(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	model := r.URL.Query().Get("model")
	token := r.URL.Query().Get("token")
	status := r.URL.Query().Get("status")
	result := s.GetLogs(tokenNames, page, model, token, status)
	writeJSON(w, result)
}

// Latency serves latency percentiles API.
func (s *StatsAPI) Latency(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	period := r.URL.Query().Get("period")
	model := r.URL.Query().Get("model")
	providerName := r.URL.Query().Get("provider")
	result := s.GetLatency(tokenNames, period, model, providerName)
	writeJSON(w, result)
}

// CostBreakdown serves cost breakdown API.
func (s *StatsAPI) CostBreakdown(w http.ResponseWriter, r *http.Request) {
	tokenNames := s.tokenNamesForUser(r)
	period := r.URL.Query().Get("period")
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "model"
	}
	result := s.GetCostBreakdown(tokenNames, period, groupBy)
	writeJSON(w, result)
}

// ProviderHealthHandler serves provider health status API.
func (s *StatsAPI) ProviderHealthHandler(w http.ResponseWriter, r *http.Request) {
	if s.healthTracker == nil {
		writeJSON(w, []provider.ProviderHealth{})
		return
	}
	writeJSON(w, s.healthTracker.Status())
}

// CacheStats serves cache statistics API.
func (s *StatsAPI) CacheStats(w http.ResponseWriter, r *http.Request) {
	if s.cache == nil {
		writeJSON(w, map[string]any{"enabled": false})
		return
	}
	stats := s.cache.Stats(r.Context())
	writeJSON(w, stats)
}

// AuditLogs serves the audit log API (admin-only).
func (s *StatsAPI) AuditLogs(w http.ResponseWriter, r *http.Request) {
	if s.auditLogger == nil {
		writeJSON(w, map[string]any{"entries": []any{}, "total": 0})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	action := r.URL.Query().Get("action")
	since := time.Now().AddDate(0, 0, -30).Unix()
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if s, err := strconv.ParseInt(sinceStr, 10, 64); err == nil {
			since = s
		}
	}
	result := s.auditLogger.Query(since, action, page, 50)
	writeJSON(w, result)
}

// DebugToggle enables/disables debug logging at runtime.
func (s *StatsAPI) DebugToggle(w http.ResponseWriter, r *http.Request) {
	if s.debugLogger == nil {
		writeJSON(w, map[string]any{"error": "debug logger not configured"})
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]string{"error": "invalid JSON"})
		return
	}
	s.debugLogger.SetEnabled(req.Enabled)
	writeJSON(w, map[string]any{"enabled": s.debugLogger.IsEnabled()})
}

// DebugStatus returns whether debug logging is enabled.
func (s *StatsAPI) DebugStatus(w http.ResponseWriter, r *http.Request) {
	enabled := false
	if s.debugLogger != nil {
		enabled = s.debugLogger.IsEnabled()
	}
	writeJSON(w, map[string]any{"enabled": enabled})
}

// DebugLogs serves paginated debug log entries.
func (s *StatsAPI) DebugLogs(w http.ResponseWriter, r *http.Request) {
	if s.debugLogger == nil {
		writeJSON(w, map[string]any{"entries": []any{}, "total": 0})
		return
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	result := s.debugLogger.Query(page, 50)
	writeJSON(w, result)
}

// DebugLogByRequestID serves a single debug log entry by request ID.
func (s *StatsAPI) DebugLogByRequestID(w http.ResponseWriter, r *http.Request) {
	if s.debugLogger == nil {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "debug logger not configured"})
		return
	}
	requestID := chi.URLParam(r, "requestID")
	entry := s.debugLogger.GetByRequestID(requestID)
	if entry == nil {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]string{"error": "not found"})
		return
	}
	writeJSON(w, entry)
}

// ValidateConfig validates the config file at the stored path.
// Returns HTML for HTMX requests, JSON otherwise.
func (s *StatsAPI) ValidateConfig(w http.ResponseWriter, r *http.Request) {
	if s.configPath == "" {
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<div class="error-msg">Config path not set</div>`))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"valid": false, "errors": []string{"config path not set"}})
		}
		return
	}
	data, err := os.ReadFile(s.configPath)
	if err != nil {
		msg := "failed to read config: " + err.Error()
		if r.Header.Get("HX-Request") == "true" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(`<div class="error-msg">` + msg + `</div>`))
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			writeJSON(w, map[string]any{"valid": false, "errors": []string{msg}})
		}
		return
	}
	errs := config.ValidateBytes(data)

	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(errs) > 0 {
			html := `<div class="error-msg">Configuration errors:<ul style="margin:4px 0 0 16px;">`
			for _, e := range errs {
				html += "<li>" + e + "</li>"
			}
			html += "</ul></div>"
			w.Write([]byte(html))
		} else {
			w.Write([]byte(`<div class="success-msg">Configuration is valid.</div>`))
		}
		return
	}

	if len(errs) > 0 {
		writeJSON(w, map[string]any{"valid": false, "errors": errs})
		return
	}
	writeJSON(w, map[string]any{"valid": true, "errors": []string{}})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
