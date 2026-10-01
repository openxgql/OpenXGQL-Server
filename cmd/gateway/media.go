package main

import (
	"io"
	"net/http"
	"net/url"
	"time"

	"flashlarry-api/internal/common"
)

var mediaClient = &http.Client{Timeout: 120 * time.Second}

func (g *Gateway) proxyMedia(w http.ResponseWriter, r *http.Request, endpoint string, userID string) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, endpoint, r.Body)
	if err != nil {
		common.Error(w, 500, "invalid media request")
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for _, key := range []string{"Range", "If-Range"} {
		if value := r.Header.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	if userID != "" {
		req.Header.Set("X-Flashlarry-User-ID", userID)
	}
	response, err := mediaClient.Do(req)
	if err != nil {
		common.Error(w, 502, "media service unavailable")
		return
	}
	defer response.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Cache-Control", "X-Content-Type-Options", "Content-Range", "Accept-Ranges"} {
		if value := response.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (g *Gateway) mediaUpload(w http.ResponseWriter, r *http.Request) {
	endpoint := g.tweetsURL + "/media/upload?" + r.URL.RawQuery
	g.proxyMedia(w, r, endpoint, actorFrom(r.Context()).UserID)
}

func (g *Gateway) mediaMetadata(w http.ResponseWriter, r *http.Request) {
	g.proxyMedia(w, r, g.tweetsURL+"/media/metadata", actorFrom(r.Context()).UserID)
}

func (g *Gateway) serveMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := url.PathUnescape(id); err != nil {
		common.Error(w, 404, "media not found")
		return
	}
	g.proxyMedia(w, r, g.tweetsURL+"/media/"+url.PathEscape(id), "")
}
