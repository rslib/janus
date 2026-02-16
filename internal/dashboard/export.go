package dashboard

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"janus/internal/auth"
	"janus/internal/storage"
)

type ExportHandler struct {
	db        *storage.DB
	authStore *auth.Store
}

func NewExportHandler(db *storage.DB, authStore *auth.Store) *ExportHandler {
	return &ExportHandler{db: db, authStore: authStore}
}

// ExportLogs exports request logs as CSV or JSON.
func (e *ExportHandler) ExportLogs(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}

	// Build query
	where := "WHERE 1=1"
	var args []any

	if from := r.URL.Query().Get("from"); from != "" {
		if ts, err := strconv.ParseInt(from, 10, 64); err == nil {
			where += " AND timestamp >= ?"
			args = append(args, ts)
		}
	}
	if to := r.URL.Query().Get("to"); to != "" {
		if ts, err := strconv.ParseInt(to, 10, 64); err == nil {
			where += " AND timestamp <= ?"
			args = append(args, ts)
		}
	}
	if model := r.URL.Query().Get("model"); model != "" {
		where += " AND model = ?"
		args = append(args, model)
	}
	if token := r.URL.Query().Get("token"); token != "" {
		where += " AND token_name = ?"
		args = append(args, token)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		where += " AND status = ?"
		args = append(args, status)
	}

	// Token filtering for non-admins
	user := auth.UserFromContext(r.Context())
	if user != nil && !user.IsAdmin {
		tokens, err := e.authStore.ListAPITokens(user.ID)
		if err != nil || len(tokens) == 0 {
			where += " AND 1=0"
		} else {
			where += " AND token_name IN ("
			for i, t := range tokens {
				if i > 0 {
					where += ","
				}
				where += "?"
				args = append(args, t.Name)
			}
			where += ")"
		}
	}

	query := `SELECT COALESCE(request_id, ''), timestamp, token_name, model, provider, provider_model,
		input_tokens, output_tokens, cost_usd, latency_ms, status,
		COALESCE(error_message, ''), streaming, cached
		FROM request_logs ` + where + ` ORDER BY timestamp DESC LIMIT 100000`

	rows, err := e.db.Query(query, args...)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type logRow struct {
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

	var results []logRow
	for rows.Next() {
		var l logRow
		var streaming, cached int
		rows.Scan(&l.RequestID, &l.Timestamp, &l.TokenName, &l.Model, &l.Provider, &l.ProviderModel,
			&l.InputTokens, &l.OutputTokens, &l.CostUSD, &l.LatencyMS, &l.Status,
			&l.ErrorMessage, &streaming, &cached)
		l.Streaming = streaming == 1
		l.Cached = cached == 1
		results = append(results, l)
	}

	now := time.Now().Format("20060102-150405")

	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=logs-%s.csv", now))
		writer := csv.NewWriter(w)
		writer.Write([]string{"request_id", "timestamp", "token_name", "model", "provider", "provider_model",
			"input_tokens", "output_tokens", "cost_usd", "latency_ms", "status", "error_message", "streaming", "cached"})
		for _, l := range results {
			writer.Write([]string{
				l.RequestID,
				strconv.FormatInt(l.Timestamp, 10),
				l.TokenName, l.Model, l.Provider, l.ProviderModel,
				strconv.Itoa(l.InputTokens), strconv.Itoa(l.OutputTokens),
				fmt.Sprintf("%.8f", l.CostUSD),
				strconv.FormatInt(l.LatencyMS, 10),
				l.Status, l.ErrorMessage,
				strconv.FormatBool(l.Streaming), strconv.FormatBool(l.Cached),
			})
		}
		writer.Flush()
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=logs-%s.json", now))
		json.NewEncoder(w).Encode(results)
	}
}

// ExportStats exports aggregated stats as CSV or JSON.
func (e *ExportHandler) ExportStats(w http.ResponseWriter, r *http.Request) {
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	statsType := r.URL.Query().Get("type")
	if statsType == "" {
		statsType = "summary"
	}

	now := time.Now().Format("20060102-150405")
	since := time.Now().AddDate(0, -1, 0).Unix()

	switch statsType {
	case "models":
		rows, err := e.db.Query(`SELECT model, COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cost_usd), 0), COALESCE(AVG(latency_ms), 0)
			FROM request_logs WHERE timestamp >= ? GROUP BY model ORDER BY requests DESC`, since)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type modelRow struct {
			Model        string  `json:"model"`
			Requests     int     `json:"requests"`
			InputTokens  int     `json:"input_tokens"`
			OutputTokens int     `json:"output_tokens"`
			CostUSD      float64 `json:"cost_usd"`
			AvgLatencyMS float64 `json:"avg_latency_ms"`
		}
		var results []modelRow
		for rows.Next() {
			var m modelRow
			rows.Scan(&m.Model, &m.Requests, &m.InputTokens, &m.OutputTokens, &m.CostUSD, &m.AvgLatencyMS)
			results = append(results, m)
		}

		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-models-%s.csv", now))
			writer := csv.NewWriter(w)
			writer.Write([]string{"model", "requests", "input_tokens", "output_tokens", "cost_usd", "avg_latency_ms"})
			for _, m := range results {
				writer.Write([]string{m.Model, strconv.Itoa(m.Requests), strconv.Itoa(m.InputTokens),
					strconv.Itoa(m.OutputTokens), fmt.Sprintf("%.8f", m.CostUSD), fmt.Sprintf("%.2f", m.AvgLatencyMS)})
			}
			writer.Flush()
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-models-%s.json", now))
			json.NewEncoder(w).Encode(results)
		}

	case "providers":
		rows, err := e.db.Query(`SELECT provider, COUNT(*) as requests,
			COALESCE(SUM(CASE WHEN status='success' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status='error' THEN 1 ELSE 0 END), 0),
			COALESCE(AVG(latency_ms), 0), COALESCE(SUM(cost_usd), 0)
			FROM request_logs WHERE timestamp >= ? GROUP BY provider ORDER BY requests DESC`, since)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type providerRow struct {
			Provider     string  `json:"provider"`
			Requests     int     `json:"requests"`
			Successes    int     `json:"successes"`
			Errors       int     `json:"errors"`
			AvgLatencyMS float64 `json:"avg_latency_ms"`
			CostUSD      float64 `json:"cost_usd"`
		}
		var results []providerRow
		for rows.Next() {
			var p providerRow
			rows.Scan(&p.Provider, &p.Requests, &p.Successes, &p.Errors, &p.AvgLatencyMS, &p.CostUSD)
			results = append(results, p)
		}

		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-providers-%s.csv", now))
			writer := csv.NewWriter(w)
			writer.Write([]string{"provider", "requests", "successes", "errors", "avg_latency_ms", "cost_usd"})
			for _, p := range results {
				writer.Write([]string{p.Provider, strconv.Itoa(p.Requests), strconv.Itoa(p.Successes),
					strconv.Itoa(p.Errors), fmt.Sprintf("%.2f", p.AvgLatencyMS), fmt.Sprintf("%.8f", p.CostUSD)})
			}
			writer.Flush()
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-providers-%s.json", now))
			json.NewEncoder(w).Encode(results)
		}

	case "tokens":
		rows, err := e.db.Query(`SELECT token_name, COUNT(*) as requests,
			COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
			COALESCE(SUM(cost_usd), 0)
			FROM request_logs WHERE timestamp >= ? GROUP BY token_name ORDER BY requests DESC`, since)
		if err != nil {
			http.Error(w, "query failed", http.StatusInternalServerError)
			return
		}
		defer rows.Close()

		type tokenRow struct {
			TokenName    string  `json:"token_name"`
			Requests     int     `json:"requests"`
			InputTokens  int     `json:"input_tokens"`
			OutputTokens int     `json:"output_tokens"`
			CostUSD      float64 `json:"cost_usd"`
		}
		var results []tokenRow
		for rows.Next() {
			var t tokenRow
			rows.Scan(&t.TokenName, &t.Requests, &t.InputTokens, &t.OutputTokens, &t.CostUSD)
			results = append(results, t)
		}

		if format == "csv" {
			w.Header().Set("Content-Type", "text/csv")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-tokens-%s.csv", now))
			writer := csv.NewWriter(w)
			writer.Write([]string{"token_name", "requests", "input_tokens", "output_tokens", "cost_usd"})
			for _, t := range results {
				writer.Write([]string{t.TokenName, strconv.Itoa(t.Requests), strconv.Itoa(t.InputTokens),
					strconv.Itoa(t.OutputTokens), fmt.Sprintf("%.8f", t.CostUSD)})
			}
			writer.Flush()
		} else {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-tokens-%s.json", now))
			json.NewEncoder(w).Encode(results)
		}

	default: // summary
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=stats-summary-%s.json", now))
		statsAPI := NewStatsAPI(e.db, e.authStore)
		result := statsAPI.GetSummary(nil)
		json.NewEncoder(w).Encode(result)
	}
}
