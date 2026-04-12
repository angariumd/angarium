package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/angariumd/angarium/internal/db"
	"github.com/angariumd/angarium/internal/models"
)

const userKey string = "user"

func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

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

		tokenHash := HashToken(token)

		var user models.User
		err := a.db.QueryRow("SELECT id, name FROM users WHERE token_hash = ?", tokenHash).Scan(&user.ID, &user.Name)
		if err != nil {
			log.Printf("Auth: invalid token for path %s", r.URL.Path)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), userKey, &user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (a *Authenticator) AgentMiddleware(sharedToken string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-Agent-Token")
		if token == "" || token != sharedToken {
			http.Error(w, "unauthorized agent", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func UserFromContext(ctx context.Context) *models.User {
	user, ok := ctx.Value(userKey).(*models.User)
	if !ok {
		return nil
	}
	return user
}

func ContextWithUser(ctx context.Context, user *models.User) context.Context {
	return context.WithValue(ctx, userKey, user)
}
