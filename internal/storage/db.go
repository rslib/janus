package storage

import (
	"database/sql"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/sqlite"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "modernc.org/sqlite"

	"janus/internal/storage/migrations"
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		// Ensure directory exists — caller should create it if needed
	}

	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_cache_size=-20000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	// Performance pragmas
	for _, pragma := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA mmap_size = 268435456",
	} {
		if _, err := db.Exec(pragma); err != nil {
			return nil, fmt.Errorf("setting pragma %s: %w", pragma, err)
		}
	}

	db.SetMaxOpenConns(1) // SQLite is single-writer
	db.SetMaxIdleConns(1)

	if err := runMigrations(db); err != nil {
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	return &DB{db}, nil
}

func runMigrations(db *sql.DB) error {
	sourceDriver, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("creating migration source: %w", err)
	}

	dbDriver, err := sqlite.WithInstance(db, &sqlite.Config{})
	if err != nil {
		return fmt.Errorf("creating migration db driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", sourceDriver, "sqlite", dbDriver)
	if err != nil {
		return fmt.Errorf("creating migrator: %w", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return fmt.Errorf("applying migrations: %w", err)
	}

	return nil
}

// CleanupOldRecords deletes records older than retentionDays.
func (db *DB) CleanupOldRecords(retentionDays int) error {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	result, err := db.Exec("DELETE FROM request_logs WHERE timestamp < ?", cutoff)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected > 0 {
		log.Printf("Cleaned up %d old request log records", affected)
	}
	return nil
}

// TodaySpend returns the total cost in USD for a given token today.
func (db *DB) TodaySpend(tokenName string) (float64, error) {
	startOfDay := time.Now().Truncate(24 * time.Hour).Unix()
	var total sql.NullFloat64
	err := db.QueryRow(
		"SELECT SUM(cost_usd) FROM request_logs WHERE token_name = ? AND timestamp >= ?",
		tokenName, startOfDay,
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// MonthSpend returns the total cost in USD for a given token this month.
func (db *DB) MonthSpend(tokenName string) (float64, error) {
	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Unix()
	var total sql.NullFloat64
	err := db.QueryRow(
		"SELECT SUM(cost_usd) FROM request_logs WHERE token_name = ? AND timestamp >= ?",
		tokenName, startOfMonth,
	).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total.Float64, nil
}

// TodaySpendAll returns today's spend for all tokens as a map.
func (db *DB) TodaySpendAll() (map[string]float64, error) {
	startOfDay := time.Now().Truncate(24 * time.Hour).Unix()
	rows, err := db.Query(
		"SELECT token_name, SUM(cost_usd) FROM request_logs WHERE timestamp >= ? GROUP BY token_name",
		startOfDay,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]float64)
	for rows.Next() {
		var name string
		var total float64
		if err := rows.Scan(&name, &total); err != nil {
			continue
		}
		result[name] = total
	}
	return result, nil
}
