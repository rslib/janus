package auth

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("opening test db: %v", err)
	}

	// Create tables
	_, err = db.Exec(`
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT UNIQUE NOT NULL,
			email TEXT DEFAULT '',
			password_hash TEXT NOT NULL,
			is_admin INTEGER DEFAULT 0,
			totp_secret TEXT DEFAULT '',
			totp_enabled INTEGER DEFAULT 0,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE sessions (
			id TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);
		CREATE TABLE api_tokens (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			key_hash TEXT NOT NULL,
			key_prefix TEXT NOT NULL,
			user_id INTEGER NOT NULL,
			rate_limit_rpm INTEGER DEFAULT 0,
			daily_budget_usd REAL DEFAULT 0,
			monthly_budget_usd REAL DEFAULT 0,
			max_concurrent INTEGER DEFAULT 0,
			created_at INTEGER NOT NULL,
			last_used_at INTEGER DEFAULT 0
		);
	`)
	if err != nil {
		t.Fatalf("creating tables: %v", err)
	}
	return db
}

func TestCreateUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	user, err := store.CreateUser("alice", "password123", true)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if user.Username != "alice" {
		t.Errorf("expected username 'alice', got '%s'", user.Username)
	}
	if !user.IsAdmin {
		t.Error("expected admin user")
	}
	if user.ID == 0 {
		t.Error("expected non-zero ID")
	}
}

func TestGetUserByUsername(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	store.CreateUser("bob", "password123", false)

	user, err := store.GetUserByUsername("bob")
	if err != nil {
		t.Fatalf("GetUserByUsername: %v", err)
	}
	if user.Username != "bob" {
		t.Errorf("expected 'bob', got '%s'", user.Username)
	}

	_, err = store.GetUserByUsername("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent user")
	}
}

func TestCheckPassword(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	store.CreateUser("charlie", "correctpassword", false)
	user, _ := store.GetUserByUsername("charlie")

	if !store.CheckPassword(user, "correctpassword") {
		t.Error("correct password should match")
	}
	if store.CheckPassword(user, "wrongpassword") {
		t.Error("wrong password should not match")
	}
}

func TestUpdatePassword(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	user, _ := store.CreateUser("dave", "oldpass12", false)

	if err := store.UpdatePassword(user.ID, "newpass12"); err != nil {
		t.Fatalf("UpdatePassword: %v", err)
	}

	user, _ = store.GetUserByUsername("dave")
	if store.CheckPassword(user, "oldpass12") {
		t.Error("old password should not work")
	}
	if !store.CheckPassword(user, "newpass12") {
		t.Error("new password should work")
	}
}

func TestDeleteUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	user1, _ := store.CreateUser("admin1", "password1234", true)
	user2, _ := store.CreateUser("user2", "password1234", false)

	// Can delete non-admin
	if err := store.DeleteUser(user2.ID); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}

	// Cannot delete last admin
	if err := store.DeleteUser(user1.ID); err == nil {
		t.Error("should not be able to delete last admin")
	}
}

func TestHasAnyUser(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	if store.HasAnyUser() {
		t.Error("should have no users initially")
	}

	store.CreateUser("first", "password1234", true)

	if !store.HasAnyUser() {
		t.Error("should have users after creation")
	}
}

func TestSessionCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	user, _ := store.CreateUser("sessuser", "password1234", false)

	sessionID, err := store.CreateSession(user.ID, 1*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	sess, err := store.GetSession(sessionID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if sess.UserID != user.ID {
		t.Errorf("expected user ID %d, got %d", user.ID, sess.UserID)
	}

	if err := store.DeleteSession(sessionID); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}

	_, err = store.GetSession(sessionID)
	if err == nil {
		t.Error("session should be deleted")
	}
}

func TestStaticTokenLookup(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	staticTokens := []StaticToken{
		{Name: "test-token", Key: "sk-test-key-12345678", RateLimitRPM: 60, DailyBudgetUSD: 10.0, MaxConcurrent: 5},
	}
	store := NewStore(db, staticTokens)

	token, err := store.LookupAPIToken("sk-test-key-12345678")
	if err != nil {
		t.Fatalf("LookupAPIToken: %v", err)
	}
	if token.Name != "test-token" {
		t.Errorf("expected 'test-token', got '%s'", token.Name)
	}
	if token.ID != -1 {
		t.Errorf("static token should have ID -1, got %d", token.ID)
	}
	if token.RateLimitRPM != 60 {
		t.Errorf("expected RPM 60, got %d", token.RateLimitRPM)
	}
	if token.MaxConcurrent != 5 {
		t.Errorf("expected max_concurrent 5, got %d", token.MaxConcurrent)
	}

	// Non-existent token
	_, err = store.LookupAPIToken("nonexistent")
	if err == nil {
		t.Error("should error on nonexistent token")
	}
}

func TestDBTokenCRUD(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewStore(db, nil)

	user, _ := store.CreateUser("tokenuser", "password1234", false)

	plainKey, token, err := store.CreateAPIToken(user.ID, "my-token", 100, 5.0)
	if err != nil {
		t.Fatalf("CreateAPIToken: %v", err)
	}
	if plainKey == "" {
		t.Error("plain key should not be empty")
	}
	if token.Name != "my-token" {
		t.Errorf("expected 'my-token', got '%s'", token.Name)
	}

	// Lookup by key
	found, err := store.LookupAPIToken(plainKey)
	if err != nil {
		t.Fatalf("LookupAPIToken: %v", err)
	}
	if found.Name != "my-token" {
		t.Errorf("expected 'my-token', got '%s'", found.Name)
	}

	// List tokens
	tokens, err := store.ListAPITokens(user.ID)
	if err != nil {
		t.Fatalf("ListAPITokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Errorf("expected 1 token, got %d", len(tokens))
	}

	// Delete
	if err := store.DeleteAPIToken(token.ID); err != nil {
		t.Fatalf("DeleteAPIToken: %v", err)
	}

	_, err = store.LookupAPIToken(plainKey)
	if err == nil {
		t.Error("token should be deleted")
	}
}

func TestSetStaticTokens(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	store := NewStore(db, nil)

	_, err := store.LookupAPIToken("key1")
	if err == nil {
		t.Error("should not find token before setting")
	}

	store.SetStaticTokens([]StaticToken{
		{Name: "new-token", Key: "key1"},
	})

	token, err := store.LookupAPIToken("key1")
	if err != nil {
		t.Fatalf("after SetStaticTokens: %v", err)
	}
	if token.Name != "new-token" {
		t.Errorf("expected 'new-token', got '%s'", token.Name)
	}
}
