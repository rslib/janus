package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"janus/internal/storage"
)

type Handlers struct {
	store         *Store
	sessionSecret string
	loginLimiter  *loginRateLimiter
	auditLogger   *storage.AuditLogger
}

func NewHandlers(store *Store, sessionSecret string) *Handlers {
	return &Handlers{
		store:         store,
		sessionSecret: sessionSecret,
		loginLimiter:  newLoginRateLimiter(),
	}
}

func (h *Handlers) SetAuditLogger(al *storage.AuditLogger) {
	h.auditLogger = al
}

func (h *Handlers) audit(r *http.Request, action, targetType, targetID, details string) {
	if h.auditLogger == nil {
		return
	}
	user := UserFromContext(r.Context())
	var userID int64
	var username string
	if user != nil {
		userID = user.ID
		username = user.Username
	}
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Real-IP"); fwd != "" {
		ip = fwd
	}
	h.auditLogger.Log(storage.AuditEntry{
		UserID:     userID,
		Username:   username,
		Action:     action,
		TargetType: targetType,
		TargetID:   targetID,
		Details:    details,
		IPAddress:  ip,
	})
}

// Login brute-force protection
type loginRateLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{attempts: make(map[string][]time.Time)}
}

func (l *loginRateLimiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-1 * time.Minute)

	// Clean old entries
	recent := l.attempts[ip][:0]
	for _, t := range l.attempts[ip] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	l.attempts[ip] = recent

	if len(recent) >= 5 {
		return false
	}
	l.attempts[ip] = append(l.attempts[ip], now)
	return true
}

func (h *Handlers) Status(w http.ResponseWriter, r *http.Request) {
	initialized := h.store.HasAnyUser()

	resp := map[string]any{
		"initialized": initialized,
		"logged_in":   false,
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil && cookie.Value != "" {
		sess, err := h.store.GetSession(cookie.Value)
		if err == nil {
			user, err := h.store.GetUserByID(sess.UserID)
			if err == nil {
				resp["logged_in"] = true
				resp["user"] = map[string]any{
					"id":           user.ID,
					"username":     user.Username,
					"is_admin":     user.IsAdmin,
					"totp_enabled": user.TOTPEnabled,
				}
			}
		}
	}

	writeJSON(w, resp)
}

func (h *Handlers) Setup(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)

	if h.store.HasAnyUser() {
		if htmx {
			writeHTMXError(w, "already initialized")
			return
		}
		writeError(w, http.StatusBadRequest, "already initialized")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Username == "" || req.Password == "" {
		if htmx {
			writeHTMXError(w, "username and password required")
			return
		}
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	if len(req.Password) < 8 {
		if htmx {
			writeHTMXError(w, "password must be at least 8 characters")
			return
		}
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	user, err := h.store.CreateUser(req.Username, req.Password, true)
	if err != nil {
		msg := "failed to create user: " + err.Error()
		if htmx {
			writeHTMXError(w, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}

	// Auto-login
	sessionID, err := h.store.CreateSession(user.ID, 7*24*time.Hour)
	if err != nil {
		if htmx {
			writeHTMXError(w, "failed to create session")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	h.audit(r, "auth.setup", "user", fmt.Sprintf("%d", user.ID), "initial setup")

	h.setSessionCookie(w, sessionID)
	if htmx {
		writeHTMXRedirect(w, "/dashboard")
		return
	}
	writeJSON(w, map[string]any{
		"user": map[string]any{
			"id":       user.ID,
			"username": user.Username,
			"is_admin": user.IsAdmin,
		},
	})
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)

	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Real-IP"); fwd != "" {
		ip = fwd
	}
	if !h.loginLimiter.allow(ip) {
		if htmx {
			writeHTMXError(w, "too many login attempts, try again in a minute")
			return
		}
		writeError(w, http.StatusTooManyRequests, "too many login attempts, try again in a minute")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	user, err := h.store.GetUserByUsername(req.Username)
	if err != nil {
		if htmx {
			writeHTMXError(w, "invalid credentials")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if !h.store.CheckPassword(user, req.Password) {
		if htmx {
			writeHTMXError(w, "invalid credentials")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}

	if user.TOTPEnabled {
		// Set pending cookie for TOTP step
		pending := h.signPendingToken(user.ID)
		http.SetCookie(w, &http.Cookie{
			Name:     "llmgw_pending",
			Value:    pending,
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			MaxAge:   300, // 5 minutes
		})
		if htmx {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, `<form id="totp-form" hx-post="/api/auth/login/totp" hx-target="#login-form-area" hx-swap="innerHTML" hx-ext="json-enc">
  <div class="form-group">
    <label>Enter your 6-digit authenticator code</label>
    <input type="text" name="code" required pattern="[0-9]{6}" maxlength="6" autocomplete="one-time-code" inputmode="numeric" style="text-align:center; font-size:1.5rem; letter-spacing:0.3em;" autofocus>
  </div>
  <button type="submit" class="btn-primary">Verify</button>
</form>`)
			return
		}
		writeJSON(w, map[string]any{"require_totp": true})
		return
	}

	sessionID, err := h.store.CreateSession(user.ID, 7*24*time.Hour)
	if err != nil {
		if htmx {
			writeHTMXError(w, "failed to create session")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	h.audit(r, "auth.login", "user", fmt.Sprintf("%d", user.ID), user.Username)

	h.setSessionCookie(w, sessionID)
	if htmx {
		writeHTMXRedirect(w, "/dashboard")
		return
	}
	writeJSON(w, map[string]any{
		"require_totp": false,
		"user": map[string]any{
			"id":       user.ID,
			"username": user.Username,
			"is_admin": user.IsAdmin,
		},
	})
}

func (h *Handlers) LoginTOTP(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)

	cookie, err := r.Cookie("llmgw_pending")
	if err != nil || cookie.Value == "" {
		if htmx {
			writeHTMXError(w, "no pending login")
			return
		}
		writeError(w, http.StatusBadRequest, "no pending login")
		return
	}

	userID, err := h.verifyPendingToken(cookie.Value)
	if err != nil {
		if htmx {
			writeHTMXError(w, "invalid or expired pending login")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid or expired pending login")
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	user, err := h.store.GetUserByID(userID)
	if err != nil {
		if htmx {
			writeHTMXError(w, "user not found")
			return
		}
		writeError(w, http.StatusBadRequest, "user not found")
		return
	}

	if !ValidateTOTPCode(user.TOTPSecret, req.Code) {
		if htmx {
			writeHTMXError(w, "invalid TOTP code")
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid TOTP code")
		return
	}

	// Clear pending cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "llmgw_pending",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	sessionID, err := h.store.CreateSession(user.ID, 7*24*time.Hour)
	if err != nil {
		if htmx {
			writeHTMXError(w, "failed to create session")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}

	h.setSessionCookie(w, sessionID)
	if htmx {
		writeHTMXRedirect(w, "/dashboard")
		return
	}
	writeJSON(w, map[string]any{
		"user": map[string]any{
			"id":       user.ID,
			"username": user.Username,
			"is_admin": user.IsAdmin,
		},
	})
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	h.audit(r, "auth.logout", "", "", "")

	cookie, err := r.Cookie(sessionCookieName)
	if err == nil {
		h.store.DeleteSession(cookie.Value)
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})

	if isHTMX(r) {
		writeHTMXRedirect(w, "/login")
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}
	writeJSON(w, map[string]any{
		"id":           user.ID,
		"username":     user.Username,
		"is_admin":     user.IsAdmin,
		"totp_enabled": user.TOTPEnabled,
	})
}

func (h *Handlers) TOTPSetup(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	key, err := GenerateTOTPKey(user.Username)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate TOTP key")
		return
	}

	if err := h.store.SetTOTPSecret(user.ID, key.Secret()); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save TOTP secret")
		return
	}

	writeJSON(w, map[string]string{
		"secret": key.Secret(),
		"uri":    key.URL(),
	})
}

func (h *Handlers) TOTPVerify(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	// Re-fetch user to get latest TOTP secret
	user, err := h.store.GetUserByID(user.ID)
	if err != nil {
		if htmx {
			writeHTMXError(w, "failed to fetch user")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to fetch user")
		return
	}

	if user.TOTPSecret == "" {
		if htmx {
			writeHTMXError(w, "TOTP not set up yet")
			return
		}
		writeError(w, http.StatusBadRequest, "TOTP not set up, call /api/auth/totp/setup first")
		return
	}

	if !ValidateTOTPCode(user.TOTPSecret, req.Code) {
		if htmx {
			writeHTMXError(w, "invalid TOTP code")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid TOTP code")
		return
	}

	if err := h.store.EnableTOTP(user.ID); err != nil {
		if htmx {
			writeHTMXError(w, "failed to enable TOTP")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to enable TOTP")
		return
	}

	h.audit(r, "totp.enable", "user", fmt.Sprintf("%d", user.ID), "")
	if htmx {
		// Trigger settings page reload to show updated TOTP status
		w.Header().Set("HX-Trigger", "settingsRefresh")
		writeHTMXSuccess(w, "Two-factor authentication enabled")
		return
	}
	writeJSON(w, map[string]string{"status": "totp_enabled"})
}

func (h *Handlers) TOTPDisable(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	if err := h.store.DisableTOTP(user.ID); err != nil {
		if htmx {
			writeHTMXError(w, "failed to disable TOTP")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to disable TOTP")
		return
	}

	h.audit(r, "totp.disable", "user", fmt.Sprintf("%d", user.ID), "")
	if htmx {
		w.Header().Set("HX-Trigger", "settingsRefresh")
		writeHTMXSuccess(w, "Two-factor authentication disabled")
		return
	}
	writeJSON(w, map[string]string{"status": "totp_disabled"})
}

// User management (admin only)

func (h *Handlers) ListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}

	// Strip sensitive fields
	type safeUser struct {
		ID          int64  `json:"id"`
		Username    string `json:"username"`
		IsAdmin     bool   `json:"is_admin"`
		TOTPEnabled bool   `json:"totp_enabled"`
		CreatedAt   int64  `json:"created_at"`
	}
	result := make([]safeUser, len(users))
	for i, u := range users {
		result[i] = safeUser{
			ID:          u.ID,
			Username:    u.Username,
			IsAdmin:     u.IsAdmin,
			TOTPEnabled: u.TOTPEnabled,
			CreatedAt:   u.CreatedAt,
		}
	}
	writeJSON(w, result)
}

func (h *Handlers) CreateUser(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		IsAdmin  bool   `json:"is_admin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Username == "" || req.Password == "" {
		if htmx {
			writeHTMXError(w, "username and password required")
			return
		}
		writeError(w, http.StatusBadRequest, "username and password required")
		return
	}
	if len(req.Password) < 8 {
		if htmx {
			writeHTMXError(w, "password must be at least 8 characters")
			return
		}
		writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	user, err := h.store.CreateUser(req.Username, req.Password, req.IsAdmin)
	if err != nil {
		msg := "failed to create user: " + err.Error()
		if htmx {
			writeHTMXError(w, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}

	h.audit(r, "user.create", "user", fmt.Sprintf("%d", user.ID), user.Username)
	if htmx {
		writeHTMXRefresh(w)
		return
	}
	writeJSON(w, map[string]any{
		"id":       user.ID,
		"username": user.Username,
		"is_admin": user.IsAdmin,
	})
}

func (h *Handlers) DeleteUser(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		if htmx {
			writeHTMXError(w, "invalid user ID")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid user ID")
		return
	}

	// Prevent deleting yourself
	user := UserFromContext(r.Context())
	if user != nil && user.ID == id {
		if htmx {
			writeHTMXError(w, "cannot delete yourself")
			return
		}
		writeError(w, http.StatusBadRequest, "cannot delete yourself")
		return
	}

	if err := h.store.DeleteUser(id); err != nil {
		if htmx {
			writeHTMXError(w, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.audit(r, "user.delete", "user", idStr, "")
	if htmx {
		writeHTMXRefresh(w)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// API Token management

func (h *Handlers) ListTokens(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var userID int64
	if !user.IsAdmin {
		userID = user.ID
	}
	// userID=0 means list all (admin)

	tokens, err := h.store.ListAPITokens(userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list tokens")
		return
	}
	if tokens == nil {
		tokens = []APIToken{}
	}
	writeJSON(w, tokens)
}

func (h *Handlers) CreateToken(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		Name           string  `json:"name"`
		RateLimitRPM   int     `json:"rate_limit_rpm"`
		DailyBudgetUSD float64 `json:"daily_budget_usd"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		if htmx {
			writeHTMXError(w, "name is required")
			return
		}
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// RateLimitRPM: 0 = unlimited, negative treated as 0
	if req.RateLimitRPM < 0 {
		req.RateLimitRPM = 0
	}

	plainKey, token, err := h.store.CreateAPIToken(user.ID, req.Name, req.RateLimitRPM, req.DailyBudgetUSD)
	if err != nil {
		msg := "failed to create token: " + err.Error()
		if htmx {
			writeHTMXError(w, msg)
			return
		}
		writeError(w, http.StatusInternalServerError, msg)
		return
	}

	h.audit(r, "token.create", "token", fmt.Sprintf("%d", token.ID), req.Name)
	if htmx {
		// Return HTML with the key display and trigger page refresh
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("HX-Trigger", "tokenCreated")
		escaped := template.HTMLEscapeString(plainKey)
		fmt.Fprintf(w, `<div class="success-msg" style="margin-bottom:12px;">Token created! Copy the key below — it won't be shown again.</div>
<div class="token-key"><code id="new-token-key">%s</code><button class="copy-btn" onclick="navigator.clipboard.writeText(document.getElementById('new-token-key').textContent)">Copy</button></div>`, escaped)
		return
	}
	writeJSON(w, map[string]any{
		"key":   plainKey,
		"token": token,
	})
}

func (h *Handlers) DeleteToken(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	idStr := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		if htmx {
			writeHTMXError(w, "invalid token ID")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid token ID")
		return
	}

	// Non-admin can only delete own tokens
	if !user.IsAdmin {
		token, err := h.store.GetAPIToken(id)
		if err != nil {
			if htmx {
				writeHTMXError(w, "token not found")
				return
			}
			writeError(w, http.StatusNotFound, "token not found")
			return
		}
		if token.UserID != user.ID {
			if htmx {
				writeHTMXError(w, "not your token")
				return
			}
			writeError(w, http.StatusForbidden, "not your token")
			return
		}
	}

	if err := h.store.DeleteAPIToken(id); err != nil {
		if htmx {
			writeHTMXError(w, "failed to delete token")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete token")
		return
	}

	h.audit(r, "token.delete", "token", idStr, "")
	if htmx {
		writeHTMXRefresh(w)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// Self-service endpoints

func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.NewPassword == "" || len(req.NewPassword) < 8 {
		if htmx {
			writeHTMXError(w, "new password must be at least 8 characters")
			return
		}
		writeError(w, http.StatusBadRequest, "new password must be at least 8 characters")
		return
	}

	// Re-fetch user to get password hash
	user, err := h.store.GetUserByID(user.ID)
	if err != nil {
		if htmx {
			writeHTMXError(w, "failed to fetch user")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to fetch user")
		return
	}

	if !h.store.CheckPassword(user, req.CurrentPassword) {
		if htmx {
			writeHTMXError(w, "current password is incorrect")
			return
		}
		writeError(w, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	if err := h.store.UpdatePassword(user.ID, req.NewPassword); err != nil {
		if htmx {
			writeHTMXError(w, "failed to update password")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update password")
		return
	}

	h.audit(r, "password.change", "user", fmt.Sprintf("%d", user.ID), "")
	if htmx {
		writeHTMXSuccess(w, "Password updated")
		return
	}
	writeJSON(w, map[string]string{"status": "password_updated"})
}

func (h *Handlers) ChangeUsername(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		NewUsername string `json:"new_username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.NewUsername == "" {
		if htmx {
			writeHTMXError(w, "username is required")
			return
		}
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}

	// Check uniqueness
	existing, err := h.store.GetUserByUsername(req.NewUsername)
	if err == nil && existing.ID != user.ID {
		if htmx {
			writeHTMXError(w, "username already taken")
			return
		}
		writeError(w, http.StatusConflict, "username already taken")
		return
	}

	if err := h.store.UpdateUsername(user.ID, req.NewUsername); err != nil {
		if htmx {
			writeHTMXError(w, "failed to update username")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update username")
		return
	}

	h.audit(r, "username.change", "user", fmt.Sprintf("%d", user.ID), req.NewUsername)
	if htmx {
		writeHTMXSuccess(w, "Username updated")
		return
	}
	writeJSON(w, map[string]string{"status": "username_updated"})
}

func (h *Handlers) ChangeEmail(w http.ResponseWriter, r *http.Request) {
	htmx := isHTMX(r)
	user := UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "not authenticated")
		return
	}

	var req struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if htmx {
			writeHTMXError(w, "invalid request")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.store.UpdateEmail(user.ID, req.Email); err != nil {
		if htmx {
			writeHTMXError(w, "failed to update email")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update email")
		return
	}

	if htmx {
		writeHTMXSuccess(w, "Email updated")
		return
	}
	writeJSON(w, map[string]string{"status": "email_updated"})
}

// Helpers

func (h *Handlers) setSessionCookie(w http.ResponseWriter, sessionID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   sessionTTLDays * 24 * 60 * 60,
	})
}

func (h *Handlers) signPendingToken(userID int64) string {
	data := fmt.Sprintf("%d:%d", userID, time.Now().Unix())
	mac := hmac.New(sha256.New, []byte(h.sessionSecret))
	mac.Write([]byte(data))
	sig := hex.EncodeToString(mac.Sum(nil))
	return data + ":" + sig
}

func (h *Handlers) verifyPendingToken(token string) (int64, error) {
	parts := strings.SplitN(token, ":", 3)
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid format")
	}

	userID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid user ID")
	}

	ts, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid timestamp")
	}

	// Check expiry (5 minutes)
	if time.Now().Unix()-ts > 300 {
		return 0, fmt.Errorf("expired")
	}

	// Verify HMAC
	data := parts[0] + ":" + parts[1]
	mac := hmac.New(sha256.New, []byte(h.sessionSecret))
	mac.Write([]byte(data))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	if !hmac.Equal([]byte(parts[2]), []byte(expectedSig)) {
		return 0, fmt.Errorf("invalid signature")
	}

	return userID, nil
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func writeHTMXError(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="error-msg">%s</div>`, template.HTMLEscapeString(msg))
}

func writeHTMXSuccess(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="success-msg">%s</div>`, template.HTMLEscapeString(msg))
}

func writeHTMXRedirect(w http.ResponseWriter, url string) {
	w.Header().Set("HX-Redirect", url)
	w.WriteHeader(http.StatusOK)
}

func writeHTMXRefresh(w http.ResponseWriter) {
	w.Header().Set("HX-Refresh", "true")
	w.WriteHeader(http.StatusOK)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
