package main

import (
	"net/http"

	"flashlarry-api/internal/common"
)

func (g *Gateway) webAccountSettings(w http.ResponseWriter, r *http.Request) {
	common.WriteJSON(w, 200, map[string]any{
		"allow_logged_out_device_personalization":               false,
		"allow_sharing_data_for_third_party_personalization":    false,
		"ext_sharing_audiospaces_listening_data_with_followers": false,
		"geo_enabled": false,
		"language":    "en",
		"nsfw_admin":  false,
		"nsfw_user":   false,
	})
}

func (g *Gateway) webGuide(w http.ResponseWriter, r *http.Request) {
	common.WriteJSON(w, 200, map[string]any{"timeline": map[string]any{"instructions": []any{}}})
}

func (g *Gateway) webFleetline(w http.ResponseWriter, r *http.Request) {
	common.WriteJSON(w, 200, map[string]any{"refresh_delay_secs": 60, "threads": []any{}})
}

func (g *Gateway) webTelemetry(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

// The schema is taken from fetch-feature-switches-B0i2E4Uc.js in the captured client.
func (g *Gateway) webFeatureSettings(w http.ResponseWriter, r *http.Request) {
	common.WriteJSON(w, 200, map[string]any{"config": map[string]any{
		"rweb_client_transaction_id_enabled": map[string]any{"value": false},
		"responsive_web_grok_enabled":        map[string]any{"value": false},
	}, "versions": map[string]any{"settings": "flashlarry-20261001"}, "impression_pointers": map[string]string{}, "impressions": []any{}})
}
