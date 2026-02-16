package storage

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

type DebugLogEntry struct {
	ID             int64  `json:"id"`
	RequestID      string `json:"request_id"`
	Timestamp      int64  `json:"timestamp"`
	TokenName      string `json:"token_name"`
	Model          string `json:"model"`
	Provider       string `json:"provider"`
	RequestBody    string `json:"request_body"`
	ResponseBody   string `json:"response_body"`
	RequestHeaders string `json:"request_headers"`
	ResponseStatus int    `json:"response_status"`
	FilePath       string `json:"-"`
}

// debugFile is the JSON structure written to disk.
type debugFile struct {
	RequestHeaders string `json:"request_headers"`
	RequestBody    string `json:"request_body"`
	ResponseBody   string `json:"response_body"`
}

type DebugLogger struct {
	db      *DB
	enabled atomic.Bool
	dataDir string
	OnWrite func()
}

func NewDebugLogger(db *DB, enabled bool, dataDir string) *DebugLogger {
	dl := &DebugLogger{db: db, dataDir: dataDir}
	dl.enabled.Store(enabled)
	return dl
}

func (d *DebugLogger) SetEnabled(v bool) {
	d.enabled.Store(v)
}

func (d *DebugLogger) IsEnabled() bool {
	return d.enabled.Load()
}

// debugLogDir returns the base directory for debug log files.
func (d *DebugLogger) debugLogDir() string {
	return filepath.Join(d.dataDir, "debug-logs")
}

// debugFilePath builds the file path for a debug log entry.
func (d *DebugLogger) debugFilePath(requestID string, ts time.Time) string {
	date := ts.Format("2006-01-02")
	return filepath.Join(d.debugLogDir(), date, requestID+".json")
}

func (d *DebugLogger) Log(entry DebugLogEntry) {
	if !d.IsEnabled() {
		return
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}

	ts := time.Unix(entry.Timestamp, 0)
	fp := d.debugFilePath(entry.RequestID, ts)

	// Write body file
	if err := os.MkdirAll(filepath.Dir(fp), 0755); err != nil {
		log.Printf("ERROR: debug log mkdir: %v", err)
		return
	}

	df := debugFile{
		RequestHeaders: entry.RequestHeaders,
		RequestBody:    entry.RequestBody,
		ResponseBody:   entry.ResponseBody,
	}
	data, err := json.Marshal(df)
	if err != nil {
		log.Printf("ERROR: debug log marshal: %v", err)
		return
	}
	if err := os.WriteFile(fp, data, 0644); err != nil {
		log.Printf("ERROR: debug log write: %v", err)
		return
	}

	// Insert metadata into DB (no bodies)
	_, err = d.db.Exec(`INSERT INTO debug_log
		(request_id, timestamp, token_name, model, provider, response_status, file_path)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		entry.RequestID, entry.Timestamp, entry.TokenName, entry.Model,
		entry.Provider, entry.ResponseStatus, fp,
	)
	if err != nil {
		log.Printf("ERROR: debug log db insert: %v", err)
	} else if d.OnWrite != nil {
		d.OnWrite()
	}
}

type DebugLogQueryResult struct {
	Entries    []DebugLogEntry `json:"entries"`
	Page       int             `json:"page"`
	TotalPages int             `json:"total_pages"`
	Total      int             `json:"total"`
}

// Query returns paginated debug log metadata (no bodies — fast).
func (d *DebugLogger) Query(page, limit int) *DebugLogQueryResult {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	offset := (page - 1) * limit

	var total int
	d.db.QueryRow("SELECT COUNT(*) FROM debug_log").Scan(&total)

	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}

	rows, err := d.db.Query(`SELECT id, request_id, timestamp, COALESCE(token_name, ''),
		COALESCE(model, ''), COALESCE(provider, ''), COALESCE(response_status, 0), COALESCE(file_path, '')
		FROM debug_log ORDER BY timestamp DESC LIMIT ? OFFSET ?`, limit, offset)
	if err != nil {
		return &DebugLogQueryResult{Entries: []DebugLogEntry{}, Page: page, TotalPages: totalPages, Total: total}
	}
	defer rows.Close()

	var entries []DebugLogEntry
	for rows.Next() {
		var e DebugLogEntry
		rows.Scan(&e.ID, &e.RequestID, &e.Timestamp, &e.TokenName,
			&e.Model, &e.Provider, &e.ResponseStatus, &e.FilePath)
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []DebugLogEntry{}
	}

	return &DebugLogQueryResult{Entries: entries, Page: page, TotalPages: totalPages, Total: total}
}

// QueryFull returns paginated debug log entries including request/response bodies read from files.
func (d *DebugLogger) QueryFull(page, limit int) *DebugLogQueryResult {
	result := d.Query(page, limit)
	for i := range result.Entries {
		d.populateFromFile(&result.Entries[i])
	}
	return result
}

// GetByRequestID returns a single debug log entry with bodies read from file.
func (d *DebugLogger) GetByRequestID(requestID string) *DebugLogEntry {
	var e DebugLogEntry
	err := d.db.QueryRow(`SELECT id, request_id, timestamp, COALESCE(token_name, ''),
		COALESCE(model, ''), COALESCE(provider, ''), COALESCE(response_status, 0), COALESCE(file_path, '')
		FROM debug_log WHERE request_id = ?`, requestID).Scan(
		&e.ID, &e.RequestID, &e.Timestamp, &e.TokenName,
		&e.Model, &e.Provider, &e.ResponseStatus, &e.FilePath)
	if err != nil {
		return nil
	}
	d.populateFromFile(&e)
	return &e
}

// populateFromFile reads body data from the debug file on disk.
// Falls back to DB columns for pre-migration entries that have no file_path.
func (d *DebugLogger) populateFromFile(e *DebugLogEntry) {
	if e.FilePath == "" {
		// Legacy entry: try reading bodies from DB columns
		d.db.QueryRow(`SELECT COALESCE(request_body, ''), COALESCE(response_body, ''), COALESCE(request_headers, '')
			FROM debug_log WHERE id = ?`, e.ID).Scan(&e.RequestBody, &e.ResponseBody, &e.RequestHeaders)
		return
	}
	data, err := os.ReadFile(e.FilePath)
	if err != nil {
		log.Printf("WARN: debug log read file %s: %v", e.FilePath, err)
		return
	}
	var df debugFile
	if err := json.Unmarshal(data, &df); err != nil {
		log.Printf("WARN: debug log parse file %s: %v", e.FilePath, err)
		return
	}
	e.RequestHeaders = df.RequestHeaders
	e.RequestBody = df.RequestBody
	e.ResponseBody = df.ResponseBody
}

// Cleanup removes debug log entries and files older than retentionDays.
func (d *DebugLogger) Cleanup(retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	cutoffUnix := cutoff.Unix()

	// Delete old DB rows
	result, err := d.db.Exec("DELETE FROM debug_log WHERE timestamp < ?", cutoffUnix)
	if err != nil {
		return fmt.Errorf("delete old debug rows: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected > 0 {
		log.Printf("Cleaned up %d old debug log entries", affected)
	}

	// Remove old date directories
	baseDir := d.debugLogDir()
	dirs, err := os.ReadDir(baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read debug log dir: %w", err)
	}

	cutoffDate := cutoff.Format("2006-01-02")
	sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name() < dirs[j].Name() })

	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		// Date directories are named YYYY-MM-DD; string comparison works
		if strings.Compare(dir.Name(), cutoffDate) < 0 {
			dirPath := filepath.Join(baseDir, dir.Name())
			if err := os.RemoveAll(dirPath); err != nil {
				log.Printf("WARN: failed to remove debug log dir %s: %v", dirPath, err)
			} else {
				log.Printf("Removed old debug log directory: %s", dir.Name())
			}
		}
	}

	return nil
}
