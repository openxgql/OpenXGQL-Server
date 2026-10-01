package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWebEmailSessionAndOrigin(t *testing.T) {
	token := strings.Repeat("a", 64)
	users := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/email/verify":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"token":"` + token + `","user":{"ID":"42","ScreenName":"tester"}}`))
		case "/auth/session":
			if r.Header.Get("Authorization") != "Bearer "+token {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ID":"42","ScreenName":"tester"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer users.Close()
	g := &Gateway{client: users.Client(), usersURL: users.URL}

	verify := httptest.NewRequest(http.MethodPost, "https://flashlarry.duckdns.org/web/auth/email/verify", strings.NewReader(`{"email":"tester@flashlarry.test","code":"777777"}`))
	verify.Header.Set("Origin", "https://flashlarry.duckdns.org")
	verify.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	g.webEmailVerify(w, verify)
	if w.Code != 200 || strings.Contains(w.Body.String(), token) {
		t.Fatalf("verify status=%d body=%s", w.Code, w.Body.String())
	}
	result := w.Result()
	cookies := result.Cookies()
	if len(cookies) != 1 || cookies[0].Name != webCookieName || !cookies[0].HttpOnly || !cookies[0].Secure {
		t.Fatalf("session cookie missing security attributes: %#v", cookies)
	}

	session := httptest.NewRequest(http.MethodGet, "https://flashlarry.duckdns.org/web/auth/session", nil)
	session.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	g.webSession(w, session)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"42"`) {
		t.Fatalf("session status=%d body=%s", w.Code, w.Body.String())
	}

	wrongOrigin := httptest.NewRequest(http.MethodPost, "https://flashlarry.duckdns.org/web/auth/email/verify", strings.NewReader(`{}`))
	wrongOrigin.Header.Set("Origin", "https://other.example")
	wrongOrigin.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	g.webEmailVerify(w, wrongOrigin)
	if w.Code != 403 {
		t.Fatalf("cross-origin verify status=%d", w.Code)
	}
}
