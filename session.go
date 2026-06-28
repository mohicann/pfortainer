package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	cookieName   = "pfsession"
	cookieMaxAge = 28800 // 8h
)

// SessionUser is carried in the HMAC-signed cookie and injected into request
// context by the auth middleware so handlers can read the caller's identity.
type SessionUser struct {
	Username string
	Role     string
}

// ── Context ────────────────────────────────────────────────────────────────────

type ctxKey int

const ctxUserKey ctxKey = 0

func withUser(r *http.Request, u SessionUser) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), ctxUserKey, u))
}

// userFrom extracts the authenticated user from the request context.
// Returns a zero SessionUser when the middleware has not populated it.
func userFrom(r *http.Request) SessionUser {
	u, _ := r.Context().Value(ctxUserKey).(SessionUser)
	return u
}

// ── Cookie ────────────────────────────────────────────────────────────────────

// setAuthCookie writes the signed "username:role" session cookie.
func setAuthCookie(w http.ResponseWriter, secret, username, role string) {
	payload := username + ":" + role
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    sign(secret, payload),
		Path:     "/",
		MaxAge:   cookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearAuthCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
}

// sessionUser verifies the cookie and returns the authenticated user.
func sessionUser(r *http.Request, secret string) (SessionUser, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return SessionUser{}, false
	}
	payload, ok := verify(secret, c.Value)
	if !ok {
		return SessionUser{}, false
	}
	username, role, found := strings.Cut(payload, ":")
	if !found || username == "" || roleRank[role] == 0 {
		return SessionUser{}, false
	}
	return SessionUser{Username: username, Role: role}, true
}

// isAuthenticated is kept for compatibility with helpers that only need a bool.
func isAuthenticated(r *http.Request, secret string) bool {
	_, ok := sessionUser(r, secret)
	return ok
}

// ── HMAC ──────────────────────────────────────────────────────────────────────

func sign(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(value))
	return value + "." + hex.EncodeToString(mac.Sum(nil))
}

func verify(secret, signed string) (string, bool) {
	// Find the last "." which separates payload from the hex HMAC.
	// The payload itself may contain "." (e.g. future extensions) but the hex
	// signature is always 64 characters, so splitting on the last dot is safe.
	idx := strings.LastIndex(signed, ".")
	if idx < 0 {
		return "", false
	}
	val, sig := signed[:idx], signed[idx+1:]
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(val))
	return val, hmac.Equal([]byte(sig), []byte(hex.EncodeToString(mac.Sum(nil))))
}
