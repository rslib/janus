package storage

import (
	"log"
	"time"
)

type RequestLog struct {
	RequestID     string
	Timestamp     int64
	TokenName     string
	Model         string
	Provider      string
	ProviderModel string
	InputTokens   int
	OutputTokens  int
	CostUSD       float64
	LatencyMS     int64
	Status        string // success, error, cached
	ErrorMessage  string
	Streaming     bool
	Cached        bool
	RequestType   string // "chat" or "embedding"
}

type AsyncLogger struct {
	db      *DB
	ch      chan RequestLog
	done    chan struct{}
	OnFlush func() // called after successful flush, if set
}

func NewAsyncLogger(db *DB, bufferSize int) *AsyncLogger {
	if bufferSize == 0 {
		bufferSize = 1000
	}
	l := &AsyncLogger{
		db:   db,
		ch:   make(chan RequestLog, bufferSize),
		done: make(chan struct{}),
	}
	go l.run()
	return l
}

func (l *AsyncLogger) Log(r RequestLog) {
	select {
	case l.ch <- r:
	default:
		log.Println("WARNING: request log buffer full, dropping entry")
	}
}

func (l *AsyncLogger) Close() {
	close(l.ch)
	<-l.done
}

func (l *AsyncLogger) run() {
	defer close(l.done)

	batch := make([]RequestLog, 0, 100)
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case r, ok := <-l.ch:
			if !ok {
				// Channel closed, flush remaining
				if len(batch) > 0 {
					l.flush(batch)
				}
				return
			}
			batch = append(batch, r)
			if len(batch) >= 100 {
				l.flush(batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				l.flush(batch)
				batch = batch[:0]
			}
		}
	}
}

func (l *AsyncLogger) flush(batch []RequestLog) {
	tx, err := l.db.Begin()
	if err != nil {
		log.Printf("ERROR: starting log transaction: %v", err)
		return
	}

	stmt, err := tx.Prepare(`INSERT INTO request_logs
		(request_id, timestamp, token_name, model, provider, provider_model, input_tokens, output_tokens, cost_usd, latency_ms, status, error_message, streaming, cached, request_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Printf("ERROR: preparing log statement: %v", err)
		tx.Rollback()
		return
	}
	defer stmt.Close()

	for _, r := range batch {
		streaming := 0
		if r.Streaming {
			streaming = 1
		}
		cached := 0
		if r.Cached {
			cached = 1
		}
		reqType := r.RequestType
		if reqType == "" {
			reqType = "chat"
		}
		_, err := stmt.Exec(
			r.RequestID, r.Timestamp, r.TokenName, r.Model, r.Provider, r.ProviderModel,
			r.InputTokens, r.OutputTokens, r.CostUSD, r.LatencyMS,
			r.Status, r.ErrorMessage, streaming, cached, reqType,
		)
		if err != nil {
			log.Printf("ERROR: inserting log: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("ERROR: committing log batch: %v", err)
		return
	}

	if l.OnFlush != nil {
		l.OnFlush()
	}
}
