package main

import (
	"net/http"
	"regexp"

	"flashlarry-api/internal/common"
)

var botUsername = regexp.MustCompile(`^[A-Za-z0-9_]{5,32}$`)

func (g *Gateway) webAuthConfig(w http.ResponseWriter, r *http.Request) {
	var config map[string]any
	if e := g.call(r.Context(), "GET", g.usersURL+"/auth/config", nil, &config); e != nil {
		common.Error(w, 503, "authentication settings unavailable")
		return
	}
	name := common.Env("TELEGRAM_BOT_USERNAME", "")
	if !botUsername.MatchString(name) {
		name = ""
	}
	if config["auth_mode"] != "telegram" {
		name = ""
	}
	config["telegram_bot_username"] = name
	common.WriteJSON(w, 200, config)
}
