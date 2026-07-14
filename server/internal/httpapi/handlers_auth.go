package httpapi

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"wanxiang-agent/server/internal/auth"
)

const adminSessionLifetime = 24 * time.Hour

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type sessionInserter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func handleAdminLogin(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid json"})
			return
		}
		var userID int64
		var hash string
		err := deps.DB.QueryRowContext(r.Context(), `select id,password_hash from users where username=?`, req.Username).Scan(&userID, &hash)
		valid, verifyErr := auth.VerifyPassword(req.Password, hash)
		if err != nil || verifyErr != nil || !valid {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "invalid credentials"})
			return
		}
		if auth.PasswordNeedsRehash(hash) {
			upgraded, err := auth.HashPassword(req.Password)
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not upgrade password"})
				return
			}
			if _, err := deps.DB.ExecContext(r.Context(), `update users set password_hash=? where id=?`, upgraded, userID); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not upgrade password"})
				return
			}
		}
		token, expiresAt, err := createAdminSession(r.Context(), deps.DB, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not create session"})
			return
		}
		setAdminSessionCookie(w, token, expiresAt)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "token": token})
	}
}

func handleAdminBootstrap(deps Dependencies) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Username == "" || req.Password == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "username and password are required"})
			return
		}

		tx, err := deps.DB.BeginTx(r.Context(), nil)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not start bootstrap"})
			return
		}
		defer tx.Rollback()
		var userCount, bootstrapCount int
		if err := tx.QueryRowContext(r.Context(), `select count(*) from users`).Scan(&userCount); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not inspect users"})
			return
		}
		if err := tx.QueryRowContext(r.Context(), `select count(*) from admin_bootstrap`).Scan(&bootstrapCount); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not inspect bootstrap state"})
			return
		}
		if userCount != 0 || bootstrapCount != 0 {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "admin bootstrap is no longer available"})
			return
		}

		now := time.Now().UTC()
		passwordHash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not hash admin password"})
			return
		}
		res, err := tx.ExecContext(r.Context(), `insert into users(username,password_hash,created_at) values(?,?,?)`, req.Username, passwordHash, now.Format(time.RFC3339Nano))
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not create admin"})
			return
		}
		userID, err := res.LastInsertId()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not identify admin"})
			return
		}
		if _, err := tx.ExecContext(r.Context(), `insert into admin_bootstrap(id,completed_at) values(1,?)`, now.Format(time.RFC3339Nano)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not complete bootstrap"})
			return
		}
		token, expiresAt, err := createAdminSession(r.Context(), tx, userID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not create session"})
			return
		}
		if err := tx.Commit(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": "could not commit bootstrap"})
			return
		}
		setAdminSessionCookie(w, token, expiresAt)
		writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "token": token})
	}
}

func createAdminSession(ctx context.Context, db sessionInserter, userID int64) (string, time.Time, error) {
	token, err := newToken()
	if err != nil {
		return "", time.Time{}, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(adminSessionLifetime)
	_, err = db.ExecContext(ctx, `insert into admin_sessions(user_id,token_hash,expires_at,created_at) values(?,?,?,?)`,
		userID, auth.HashSecret(token), expiresAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expiresAt, nil
}

func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	if len(buf) == 0 {
		return "", errors.New("empty token")
	}
	return hex.EncodeToString(buf), nil
}

func setAdminSessionCookie(w http.ResponseWriter, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(adminSessionLifetime.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
