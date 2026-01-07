package auth

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
)

type contextKey string

const userKey contextKey = "user"

type Authenticator struct {
	db *db.DB
}

func NewAuthenticator(db *db.DB) *Authenticator {
	return &Authenticator{db: db}
}

func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "missing auth header", http.StatusUnauthorized)
			return
		}

		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == authHeader {
			http.Error(w, "invalid auth header format", http.StatusUnauthorized)
			return
		}

		// Verify token against user records
		var user models.User
		err := a.db.QueryRow("SELECT id, name FROM users WHERE token_hash = ?", token).Scan(&user.ID, &user.Name)
		if err != nil {
			log.Printf("Auth: invalid token %q for path %s: %v", token, r.URL.Path, err)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, &user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func UserFromContext(ctx context.Context) *models.User {
	user, ok := ctx.Value(userKey).(*models.User)
	if !ok {
		return nil
	}
	return user
}
