package httpapi

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	"wanxiang-agent/server/internal/agents"
	"wanxiang-agent/server/internal/auth"
)

const adminSessionCookie = "wanxiang_admin_session"

type identityKey string

const (
	adminIdentityKey identityKey = "admin_identity"
	agentIdentityKey identityKey = "agent_identity"
)

func RequireAdmin(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			if token == "" {
				if cookie, err := r.Cookie(adminSessionCookie); err == nil {
					token = cookie.Value
				}
			}
			username, ok := validAdminSession(r.Context(), db, token)
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid or expired admin session"})
				return
			}
			ctx := context.WithValue(r.Context(), adminIdentityKey, username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func RequireAgent(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			agentName, ok := validAgentToken(r.Context(), db, bearerToken(r))
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid or expired agent token"})
				return
			}
			ctx := context.WithValue(r.Context(), agentIdentityKey, agentName)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func AdminIdentity(ctx context.Context) (string, bool) {
	username, ok := ctx.Value(adminIdentityKey).(string)
	return username, ok && username != ""
}

func AgentIdentity(ctx context.Context) (string, bool) {
	agentName, ok := ctx.Value(agentIdentityKey).(string)
	return agentName, ok && agentName != ""
}

func bearerToken(r *http.Request) string {
	parts := strings.Fields(r.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func validAdminSession(ctx context.Context, db *sql.DB, token string) (string, bool) {
	if token == "" || db == nil {
		return "", false
	}
	var username, expiresAt string
	err := db.QueryRowContext(ctx, `select u.username,s.expires_at from admin_sessions s join users u on u.id=s.user_id where s.token_hash=?`, auth.HashSecret(token)).Scan(&username, &expiresAt)
	if err != nil {
		return "", false
	}
	return username, expiryValid(expiresAt)
}

func validAgentToken(ctx context.Context, db *sql.DB, token string) (string, bool) {
	if token == "" || db == nil {
		return "", false
	}
	var agentName string
	var expiresAt sql.NullString
	err := db.QueryRowContext(ctx, `select agent_name,expires_at from agent_tokens where token_hash=? order by id desc limit 1`, auth.HashSecret(token)).Scan(&agentName, &expiresAt)
	if err != nil || agents.ValidateName(agentName) != nil {
		return "", false
	}
	return agentName, !expiresAt.Valid || expiresAt.String == "" || expiryValid(expiresAt.String)
}

func expiryValid(value string) bool {
	expiresAt, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && time.Now().UTC().Before(expiresAt)
}
