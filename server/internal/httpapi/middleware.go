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

type AgentPrincipalValue struct {
	Name string
	Role string
}

func RequireAdmin(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := bearerToken(r)
			username, ok := validAdminSession(r.Context(), db, token)
			if !ok {
				if cookie, err := r.Cookie(adminSessionCookie); err == nil && cookie.Value != token {
					username, ok = validAdminSession(r.Context(), db, cookie.Value)
				}
			}
			if !ok {
				clearAdminSessionCookie(w)
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid or expired admin session"})
				return
			}
			ctx := context.WithValue(r.Context(), adminIdentityKey, username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func clearAdminSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0).UTC(),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func RequireAgent(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, ok := validAgentToken(r.Context(), db, bearerToken(r))
			if !ok {
				writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid or expired agent token"})
				return
			}
			ctx := context.WithValue(r.Context(), agentIdentityKey, principal)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func AdminIdentity(ctx context.Context) (string, bool) {
	username, ok := ctx.Value(adminIdentityKey).(string)
	return username, ok && username != ""
}

func AgentIdentity(ctx context.Context) (string, bool) {
	principal, ok := AgentPrincipal(ctx)
	return principal.Name, ok
}

func AgentPrincipal(ctx context.Context) (AgentPrincipalValue, bool) {
	principal, ok := ctx.Value(agentIdentityKey).(AgentPrincipalValue)
	return principal, ok && principal.Name != "" && principal.Role != ""
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

func validAgentToken(ctx context.Context, db *sql.DB, token string) (AgentPrincipalValue, bool) {
	if token == "" || db == nil {
		return AgentPrincipalValue{}, false
	}
	var principal AgentPrincipalValue
	var expiresAt sql.NullString
	err := db.QueryRowContext(ctx, `select t.agent_name,r.role,t.expires_at from agent_tokens t join agent_registry r on r.name=t.agent_name where t.token_hash=? order by t.id desc limit 1`, auth.HashSecret(token)).Scan(&principal.Name, &principal.Role, &expiresAt)
	if err != nil || agents.ValidateName(principal.Name) != nil || strings.TrimSpace(principal.Role) == "" {
		return AgentPrincipalValue{}, false
	}
	return principal, !expiresAt.Valid || expiresAt.String == "" || expiryValid(expiresAt.String)
}

func expiryValid(value string) bool {
	expiresAt, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && time.Now().UTC().Before(expiresAt)
}
