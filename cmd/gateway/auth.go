package main

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"flashlarry-api/internal/common"
)

type actorKey struct{}

type Actor struct{ UserID string }

var webSessionToken = regexp.MustCompile(`^[a-f0-9]{64}$`)

func actorFrom(ctx context.Context) *Actor {
	actor, _ := ctx.Value(actorKey{}).(*Actor)
	return actor
}

func (g *Gateway) requiredAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if cookie, err := r.Cookie("flashlarry_session"); err == nil && webSessionToken.MatchString(cookie.Value) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && !sameOrigin(r) {
				common.Error(w, 403, "same-origin request required")
				return
			}
			header = "Bearer " + cookie.Value
		}
		if !strings.HasPrefix(header, "Bearer ") || !webSessionToken.MatchString(strings.TrimPrefix(header, "Bearer ")) {
			common.Error(w, 401, "valid Bearer session required")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", g.usersURL+"/auth/session", nil)
		if err != nil {
			common.Error(w, 500, "internal error")
			return
		}
		req.Header.Set("Authorization", header)
		response, err := g.client.Do(req)
		if err != nil {
			common.Error(w, 503, "authentication unavailable")
			return
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			common.Error(w, 401, "invalid session")
			return
		}
		var user User
		if err := json.NewDecoder(response.Body).Decode(&user); err != nil {
			common.Error(w, 503, "authentication unavailable")
			return
		}
		ctx := context.WithValue(r.Context(), actorKey{}, &Actor{UserID: user.ID})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	return origin != "" && origin == "https://"+r.Host
}
