package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	PasswordHash string `json:"-"`
	IsAdmin      bool   `json:"is_admin"`
	TOTPSecret   string `json:"-"`
	TOTPEnabled  bool   `json:"totp_enabled"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

type Session struct {
	ID        string
	UserID    int64
	CreatedAt int64
	ExpiresAt int64
}

type APIToken struct {
	ID               int64   `json:"id"`
	Name             string  `json:"name"`
	KeyPrefix        string  `json:"key_prefix"`
	KeyHash          string  `json:"-"`
	UserID           int64   `json:"user_id"`
	RateLimitRPM     int     `json:"rate_limit_rpm"`
	DailyBudgetUSD   float64 `json:"daily_budget_usd"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	MaxConcurrent    int     `json:"max_concurrent"`
	CreatedAt        int64   `json:"created_at"`
	LastUsedAt       int64   `json:"last_used_at"`
}

// StaticToken represents a token defined in config (checked in-memory, never stored in DB).
type StaticToken struct {
	Name             string
	Key              string
	RateLimitRPM     int
	DailyBudgetUSD   float64
	MonthlyBudgetUSD float64
	MaxConcurrent    int
}

type Store struct {
	db           *sql.DB
	staticTokens []StaticToken
}

func NewStore(db *sql.DB, staticTokens []StaticToken) *Store {
	return &Store{db: db, staticTokens: staticTokens}
}

// SetStaticTokens updates the static tokens list (used for config hot-reload).
func (s *Store) SetStaticTokens(tokens []StaticToken) {
	s.staticTokens = tokens
}

func (s *Store) HasAnyUser() bool {
	var count int
	s.db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
	return count > 0
}

func (s *Store) CreateUser(username, password string, isAdmin bool) (*User, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	now := time.Now().Unix()
	adminInt := 0
	if isAdmin {
		adminInt = 1
	}

	result, err := s.db.Exec(
		"INSERT INTO users (username, password_hash, is_admin, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		username, string(hash), adminInt, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	id, _ := result.LastInsertId()
	return &User{
		ID:        id,
		Username:  username,
		IsAdmin:   isAdmin,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (s *Store) GetUserByUsername(username string) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		"SELECT id, username, email, password_hash, is_admin, totp_secret, totp_enabled, created_at, updated_at FROM users WHERE username = ?",
		username,
	))
}

func (s *Store) GetUserByID(id int64) (*User, error) {
	return s.scanUser(s.db.QueryRow(
		"SELECT id, username, email, password_hash, is_admin, totp_secret, totp_enabled, created_at, updated_at FROM users WHERE id = ?",
		id,
	))
}

func (s *Store) scanUser(row *sql.Row) (*User, error) {
	var u User
	var isAdmin, totpEnabled int
	var totpSecret sql.NullString
	var email sql.NullString
	err := row.Scan(&u.ID, &u.Username, &email, &u.PasswordHash, &isAdmin, &totpSecret, &totpEnabled, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	u.Email = email.String
	u.IsAdmin = isAdmin == 1
	u.TOTPEnabled = totpEnabled == 1
	u.TOTPSecret = totpSecret.String
	return &u, nil
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query("SELECT id, username, email, password_hash, is_admin, totp_secret, totp_enabled, created_at, updated_at FROM users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var u User
		var isAdmin, totpEnabled int
		var totpSecret sql.NullString
		var email sql.NullString
		if err := rows.Scan(&u.ID, &u.Username, &email, &u.PasswordHash, &isAdmin, &totpSecret, &totpEnabled, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, err
		}
		u.Email = email.String
		u.IsAdmin = isAdmin == 1
		u.TOTPEnabled = totpEnabled == 1
		u.TOTPSecret = totpSecret.String
		users = append(users, u)
	}
	return users, nil
}

func (s *Store) DeleteUser(id int64) error {
	// Prevent deleting the last admin
	var adminCount int
	s.db.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin = 1").Scan(&adminCount)

	var isAdmin int
	s.db.QueryRow("SELECT is_admin FROM users WHERE id = ?", id).Scan(&isAdmin)
	if isAdmin == 1 && adminCount <= 1 {
		return fmt.Errorf("cannot delete the last admin user")
	}

	_, err := s.db.Exec("DELETE FROM users WHERE id = ?", id)
	return err
}

func (s *Store) UpdatePassword(userID int64, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), 12)
	if err != nil {
		return fmt.Errorf("hashing password: %w", err)
	}
	_, err = s.db.Exec("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?", string(hash), time.Now().Unix(), userID)
	return err
}

func (s *Store) CheckPassword(user *User, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) == nil
}

func (s *Store) SetTOTPSecret(userID int64, secret string) error {
	_, err := s.db.Exec("UPDATE users SET totp_secret = ?, updated_at = ? WHERE id = ?", secret, time.Now().Unix(), userID)
	return err
}

func (s *Store) EnableTOTP(userID int64) error {
	_, err := s.db.Exec("UPDATE users SET totp_enabled = 1, updated_at = ? WHERE id = ?", time.Now().Unix(), userID)
	return err
}

func (s *Store) DisableTOTP(userID int64) error {
	_, err := s.db.Exec("UPDATE users SET totp_enabled = 0, totp_secret = '', updated_at = ? WHERE id = ?", time.Now().Unix(), userID)
	return err
}

// Session management

func (s *Store) CreateSession(userID int64, ttl time.Duration) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating session ID: %w", err)
	}
	id := hex.EncodeToString(b)
	now := time.Now().Unix()
	expiresAt := time.Now().Add(ttl).Unix()

	_, err := s.db.Exec(
		"INSERT INTO sessions (id, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)",
		id, userID, now, expiresAt,
	)
	if err != nil {
		return "", fmt.Errorf("creating session: %w", err)
	}
	return id, nil
}

func (s *Store) GetSession(sessionID string) (*Session, error) {
	var sess Session
	err := s.db.QueryRow(
		"SELECT id, user_id, created_at, expires_at FROM sessions WHERE id = ? AND expires_at > ?",
		sessionID, time.Now().Unix(),
	).Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt)
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE id = ?", id)
	return err
}

func (s *Store) CleanExpiredSessions() error {
	_, err := s.db.Exec("DELETE FROM sessions WHERE expires_at <= ?", time.Now().Unix())
	return err
}

// API Token management

func (s *Store) CreateAPIToken(userID int64, name string, rateLimitRPM int, dailyBudgetUSD float64) (string, *APIToken, error) {
	// Generate sk- prefixed random key
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, fmt.Errorf("generating token: %w", err)
	}
	plainKey := "sk-" + hex.EncodeToString(b)
	keyPrefix := plainKey[:11] // "sk-" + first 8 hex chars

	hash := sha256.Sum256([]byte(plainKey))
	keyHash := hex.EncodeToString(hash[:])

	now := time.Now().Unix()
	result, err := s.db.Exec(
		"INSERT INTO api_tokens (name, key_hash, key_prefix, user_id, rate_limit_rpm, daily_budget_usd, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		name, keyHash, keyPrefix, userID, rateLimitRPM, dailyBudgetUSD, now,
	)
	if err != nil {
		return "", nil, fmt.Errorf("creating API token: %w", err)
	}

	id, _ := result.LastInsertId()
	token := &APIToken{
		ID:             id,
		Name:           name,
		KeyPrefix:      keyPrefix,
		KeyHash:        keyHash,
		UserID:         userID,
		RateLimitRPM:   rateLimitRPM,
		DailyBudgetUSD: dailyBudgetUSD,
		CreatedAt:      now,
	}
	return plainKey, token, nil
}

func (s *Store) LookupAPIToken(key string) (*APIToken, error) {
	// Check static tokens first (from config, never stored in DB)
	for _, st := range s.staticTokens {
		if st.Key == key {
			prefix := st.Key
			if len(prefix) > 11 {
				prefix = prefix[:11]
			}
			return &APIToken{
				ID:               -1, // sentinel: static token
				Name:             st.Name,
				KeyPrefix:        prefix,
				RateLimitRPM:     st.RateLimitRPM,
				DailyBudgetUSD:   st.DailyBudgetUSD,
				MonthlyBudgetUSD: st.MonthlyBudgetUSD,
				MaxConcurrent:    st.MaxConcurrent,
			}, nil
		}
	}

	// Fall back to DB tokens
	hash := sha256.Sum256([]byte(key))
	keyHash := hex.EncodeToString(hash[:])

	var t APIToken
	err := s.db.QueryRow(
		"SELECT id, name, key_hash, key_prefix, user_id, rate_limit_rpm, daily_budget_usd, COALESCE(monthly_budget_usd, 0), COALESCE(max_concurrent, 0), created_at, last_used_at FROM api_tokens WHERE key_hash = ?",
		keyHash,
	).Scan(&t.ID, &t.Name, &t.KeyHash, &t.KeyPrefix, &t.UserID, &t.RateLimitRPM, &t.DailyBudgetUSD, &t.MonthlyBudgetUSD, &t.MaxConcurrent, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) ListAPITokens(userID int64) ([]APIToken, error) {
	// Include static tokens (shown for all users, not deletable)
	var tokens []APIToken
	for _, st := range s.staticTokens {
		prefix := st.Key
		if len(prefix) > 11 {
			prefix = prefix[:11]
		}
		tokens = append(tokens, APIToken{
			ID:               -1, // sentinel: static token
			Name:             st.Name,
			KeyPrefix:        prefix,
			RateLimitRPM:     st.RateLimitRPM,
			DailyBudgetUSD:   st.DailyBudgetUSD,
			MonthlyBudgetUSD: st.MonthlyBudgetUSD,
			MaxConcurrent:    st.MaxConcurrent,
		})
	}

	// DB tokens
	var rows *sql.Rows
	var err error
	if userID == 0 {
		rows, err = s.db.Query("SELECT id, name, key_hash, key_prefix, user_id, rate_limit_rpm, daily_budget_usd, COALESCE(monthly_budget_usd, 0), COALESCE(max_concurrent, 0), created_at, last_used_at FROM api_tokens ORDER BY id")
	} else {
		rows, err = s.db.Query("SELECT id, name, key_hash, key_prefix, user_id, rate_limit_rpm, daily_budget_usd, COALESCE(monthly_budget_usd, 0), COALESCE(max_concurrent, 0), created_at, last_used_at FROM api_tokens WHERE user_id = ? ORDER BY id", userID)
	}
	if err != nil {
		return tokens, nil
	}
	defer rows.Close()

	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.KeyHash, &t.KeyPrefix, &t.UserID, &t.RateLimitRPM, &t.DailyBudgetUSD, &t.MonthlyBudgetUSD, &t.MaxConcurrent, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return tokens, nil
		}
		tokens = append(tokens, t)
	}
	return tokens, nil
}

func (s *Store) DeleteAPIToken(id int64) error {
	_, err := s.db.Exec("DELETE FROM api_tokens WHERE id = ?", id)
	return err
}

func (s *Store) GetAPIToken(id int64) (*APIToken, error) {
	var t APIToken
	err := s.db.QueryRow(
		"SELECT id, name, key_hash, key_prefix, user_id, rate_limit_rpm, daily_budget_usd, COALESCE(monthly_budget_usd, 0), COALESCE(max_concurrent, 0), created_at, last_used_at FROM api_tokens WHERE id = ?",
		id,
	).Scan(&t.ID, &t.Name, &t.KeyHash, &t.KeyPrefix, &t.UserID, &t.RateLimitRPM, &t.DailyBudgetUSD, &t.MonthlyBudgetUSD, &t.MaxConcurrent, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) UpdateAPITokenLastUsed(id int64) {
	s.db.Exec("UPDATE api_tokens SET last_used_at = ? WHERE id = ?", time.Now().Unix(), id)
}

func (s *Store) UpdateUsername(userID int64, newUsername string) error {
	_, err := s.db.Exec("UPDATE users SET username = ?, updated_at = ? WHERE id = ?", newUsername, time.Now().Unix(), userID)
	return err
}

func (s *Store) UpdateEmail(userID int64, email string) error {
	_, err := s.db.Exec("UPDATE users SET email = ?, updated_at = ? WHERE id = ?", email, time.Now().Unix(), userID)
	return err
}
