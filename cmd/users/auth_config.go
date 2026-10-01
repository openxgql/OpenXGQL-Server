package main

import (
	"flashlarry-api/internal/common"
	"flashlarry-api/internal/siteconfig"
	"net/http"
)

func (a *App) authConfig(w http.ResponseWriter, r *http.Request) {
	c, e := siteconfig.Load(r.Context(), a.db)
	if e != nil {
		common.Error(w, 503, "authentication settings unavailable")
		return
	}
	common.WriteJSON(w, 200, map[string]any{"auth_mode": c.AuthMode, "revision": c.AuthRevision, "registration": c.AuthMode == "email"})
}
func (a *App) telegramOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := siteconfig.Load(r.Context(), a.db)
		if e != nil {
			common.Error(w, 503, "authentication settings unavailable")
			return
		}
		if c.AuthMode != "telegram" {
			common.Error(w, 403, "Telegram bot is disabled; use email login")
			return
		}
		next.ServeHTTP(w, r)
	})
}
