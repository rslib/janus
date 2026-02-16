package proxy

import (
	"net/http"
	"strings"

	"janus/internal/auth"
)

type AuthMiddleware struct {
	authStore *auth.Store
}

func NewAuthMiddleware(authStore *auth.Store) *AuthMiddleware {
	return &AuthMiddleware{authStore: authStore}
}

// Authenticate validates the bearer token against the DB and sets token info in context.
func (a *AuthMiddleware) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := r.Header.Get("Authorization")
		if !strings.HasPrefix(hdr, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "missing or invalid Authorization header")
			return
		}
		key := strings.TrimPrefix(hdr, "Bearer ")

		token, err := a.authStore.LookupAPIToken(key)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid API key")
			return
		}

		// Update last used asynchronously (skip for static tokens)
		if token.ID > 0 {
			go a.authStore.UpdateAPITokenLastUsed(token.ID)
		}

		ctx := withTokenName(r.Context(), token.Name)
		ctx = withAPIToken(ctx, token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
