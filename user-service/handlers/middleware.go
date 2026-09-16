package handlers

import (
	"context"
	"net/http"
	"strings"

	"user-service/utils"
)

type contextKey string

const userIDContextKey contextKey = "userID"

// AuthMiddleware validates the Bearer JWT in the Authorization header and
// injects the authenticated user ID into the request context.
func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			writeError(w, http.StatusUnauthorized, "missing authorization header")
			return
		}

		tokenString := strings.TrimPrefix(authHeader, "Bearer ")
		claims, err := utils.ParseToken(tokenString)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}

		ctx := context.WithValue(r.Context(), userIDContextKey, claims.UserID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func userIDFromContext(ctx context.Context) (uint, bool) {
	id, ok := ctx.Value(userIDContextKey).(uint)
	return id, ok
}

// WithUserID injects a user ID into the request context the same way
// AuthMiddleware does. Exported for use by handler unit tests.
func WithUserID(r *http.Request, userID uint) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), userIDContextKey, userID))
}
