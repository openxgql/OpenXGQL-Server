package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"flashlarry-api/internal/common"
)

const webCookieName = "flashlarry_session"

func setViewerMarker(w http.ResponseWriter, userID string) {
	cookie := &http.Cookie{Name: "twid", Path: "/", Secure: true, SameSite: http.SameSiteLaxMode}
	if userID == "" {
		cookie.MaxAge = -1
	} else {
		cookie.Value = "u=" + userID
		cookie.Expires = time.Now().Add(7 * 24 * time.Hour)
	}
	http.SetCookie(w, cookie)
}

func (g *Gateway) webEmailStart(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		common.Error(w, 403, "same-origin request required")
		return
	}
	g.forwardWebAuth(w, r, "/auth/email/start", false)
}

func (g *Gateway) webEmailVerify(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		common.Error(w, 403, "same-origin request required")
		return
	}
	g.forwardWebAuth(w, r, "/auth/email/verify", true)
}

func (g *Gateway) forwardWebAuth(w http.ResponseWriter, r *http.Request, endpoint string, setCookie bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	data, err := io.ReadAll(r.Body)
	if err != nil || !json.Valid(data) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		common.Error(w, 400, "JSON body required")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.usersURL+endpoint, strings.NewReader(string(data)))
	if err != nil {
		common.Error(w, 500, "internal error")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-IP", r.Header.Get("X-Forwarded-For"))
	client := *g.client
	client.Timeout = 12 * time.Second
	response, err := client.Do(req)
	if err != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	defer response.Body.Close()
	var body map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<10)).Decode(&body); err != nil {
		common.Error(w, 502, "invalid authentication response")
		return
	}
	if response.StatusCode >= 400 {
		common.WriteJSON(w, response.StatusCode, body)
		return
	}
	if setCookie {
		token, _ := body["token"].(string)
		if !webSessionToken.MatchString(token) {
			common.Error(w, 502, "invalid session token")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: webCookieName, Value: token, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, Expires: time.Now().Add(7 * 24 * time.Hour)})
		delete(body, "token")
	}
	common.WriteJSON(w, response.StatusCode, body)
}

func (g *Gateway) webSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(webCookieName)
	if err != nil || !webSessionToken.MatchString(cookie.Value) {
		setViewerMarker(w, "")
		common.Error(w, 401, "session required")
		return
	}
	var user User
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, g.usersURL+"/auth/session", nil)
	req.Header.Set("Authorization", "Bearer "+cookie.Value)
	response, err := g.client.Do(req)
	if err != nil {
		common.Error(w, 503, "authentication unavailable")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&user) != nil {
		setViewerMarker(w, "")
		common.Error(w, 401, "invalid session")
		return
	}
	setViewerMarker(w, user.ID)
	common.WriteJSON(w, 200, map[string]any{"user": user})
}

func (g *Gateway) webLogout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		common.Error(w, 403, "same-origin request required")
		return
	}
	if cookie, err := r.Cookie(webCookieName); err == nil && webSessionToken.MatchString(cookie.Value) {
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, g.usersURL+"/auth/logout", nil)
		req.Header.Set("Authorization", "Bearer "+cookie.Value)
		response, err := g.client.Do(req)
		if err == nil {
			_ = response.Body.Close()
		}
	}
	http.SetCookie(w, &http.Cookie{Name: webCookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode})
	setViewerMarker(w, "")
	w.WriteHeader(http.StatusNoContent)
}
