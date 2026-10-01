package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMobileOAuthToken(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader("grant_type=client_credentials"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	(&Gateway{}).mobileOAuthToken(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body) != 2 || body["token_type"] != "bearer" || len(body["access_token"]) != 48 {
		t.Fatalf("unexpected OAuth response shape: %#v", body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("app token response must not be cached")
	}
}

func TestMobileOAuthTokenRejectsUnsupportedGrant(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/oauth2/token", strings.NewReader("grant_type=password"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	(&Gateway{}).mobileOAuthToken(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", response.Code)
	}
}
