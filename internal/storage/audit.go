package storage

import (
	"log"
	"time"
)

type AuditEntry struct {
	ID         int64  `json:"id"`
	Timestamp  int64  `json:"timestamp"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username"`
	Action     string `json:"action"`
	TargetType string `json:"target_type"`
	TargetID   string `json:"target_id"`
	Details    string `json:"details"`
	IPAddress  string `json:"ip_address"`
	RequestID  string `json:"request_id"`
}

type AuditLogger struct {
	db      *DB
	OnWrite func()
}

func NewAuditLogger(db *DB) *AuditLogger {
	return &AuditLogger{db: db}
}

func (a *AuditLogger) Log(entry AuditEntry) {
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}
	_, err := a.db.Exec(`INSERT INTO audit_log
		(timestamp, user_id, username, action, target_type, target_id, details, ip_address, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.Timestamp, entry.UserID, entry.Username, entry.Action,
		entry.TargetType, entry.TargetID, entry.Details, entry.IPAddress, entry.RequestID,
	)
	if err != nil {
		log.Printf("ERROR: audit log: %v", err)
	} else if a.OnWrite != nil {
		a.OnWrite()
	}
}

type AuditQueryResult struct {
	Entries    []AuditEntry `json:"entries"`
	Page       int          `json:"page"`
	TotalPages int          `json:"total_pages"`
	Total      int          `json:"total"`
}

func (a *AuditLogger) Query(since int64, action string, page, limit int) *AuditQueryResult {
	if page < 1 {
		page = 1
	}
	if limit <= 0 {
		limit = 50
	}
	offset := (page - 1) * limit

	where := "WHERE timestamp >= ?"
	args := []any{since}

	if action != "" {
		where += " AND action = ?"
		args = append(args, action)
	}

	var total int
	countArgs := make([]any, len(args))
	copy(countArgs, args)
	a.db.QueryRow("SELECT COUNT(*) FROM audit_log "+where, countArgs...).Scan(&total)

	totalPages := (total + limit - 1) / limit
	if totalPages < 1 {
		totalPages = 1
	}

	query := `SELECT id, timestamp, COALESCE(user_id, 0), username, action,
		COALESCE(target_type, ''), COALESCE(target_id, ''), COALESCE(details, ''),
		COALESCE(ip_address, ''), COALESCE(request_id, '')
		FROM audit_log ` + where + ` ORDER BY timestamp DESC LIMIT ? OFFSET ?`
	args = append(args, limit, offset)

	rows, err := a.db.Query(query, args...)
	if err != nil {
		return &AuditQueryResult{Entries: []AuditEntry{}, Page: page, TotalPages: totalPages, Total: total}
	}
	defer rows.Close()

	var entries []AuditEntry
	for rows.Next() {
		var e AuditEntry
		rows.Scan(&e.ID, &e.Timestamp, &e.UserID, &e.Username, &e.Action,
			&e.TargetType, &e.TargetID, &e.Details, &e.IPAddress, &e.RequestID)
		entries = append(entries, e)
	}
	if entries == nil {
		entries = []AuditEntry{}
	}

	return &AuditQueryResult{Entries: entries, Page: page, TotalPages: totalPages, Total: total}
}
