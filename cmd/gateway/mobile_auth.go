package main

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/common"
)

type mobileFlow struct {
	email   string
	stage   int
	expires time.Time
}

func randomToken() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func (g *Gateway) mobileOAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" {
		common.Error(w, http.StatusBadRequest, "unsupported grant_type")
		return
	}
	token, err := randomToken()
	if err != nil {
		common.Error(w, http.StatusInternalServerError, "app token unavailable")
		return
	}
	w.Header().Set("Pragma", "no-cache")
	common.WriteJSON(w, http.StatusOK, map[string]string{"token_type": "bearer", "access_token": token})
}

func (g *Gateway) mobileGuest(w http.ResponseWriter, r *http.Request) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		common.Error(w, 500, "guest token unavailable")
		return
	}
	token := strconv.FormatUint(binary.BigEndian.Uint64(value[:])%900000000000000000+100000000000000000, 10)
	common.WriteJSON(w, 200, map[string]string{"guest_token": token})
}

func (g *Gateway) storeFlow(token string, flow mobileFlow) bool {
	g.flowMu.Lock()
	defer g.flowMu.Unlock()
	for key, item := range g.flows {
		if time.Now().After(item.expires) {
			delete(g.flows, key)
		}
	}
	if _, exists := g.flows[token]; !exists && len(g.flows) >= 10000 {
		return false
	}
	g.flows[token] = flow
	return true
}

func (g *Gateway) loadFlow(token string) (mobileFlow, bool) {
	g.flowMu.Lock()
	defer g.flowMu.Unlock()
	flow, found := g.flows[token]
	return flow, found && time.Now().Before(flow.expires)
}

func (g *Gateway) forgetFlow(token string) {
	g.flowMu.Lock()
	delete(g.flows, token)
	g.flowMu.Unlock()
}

func mobileEmailEntrySubtask() map[string]any {
	return map[string]any{"subtask_id": "EnterEmail", "enter_email": map[string]any{
		"primary_text": map[string]any{"text": "Email", "entities": []any{}},
		"hint_text":    "name@flashlarry.test",
		"next_link":    map[string]any{"link_type": "task", "link_id": "next_link", "label": "Next"},
	}}
}

func mobileEmailSubtask() map[string]any {
	return map[string]any{"subtask_id": "EmailVerification", "email_verification": map[string]any{
		"primary_text": map[string]any{"text": "Enter the code from Telegram", "entities": []any{}},
		"code_length":  6,
		"next_link":    map[string]any{"link_type": "task", "link_id": "next_link", "label": "Next"},
	}}
}

func mobileFlowResponse(w http.ResponseWriter, token string, subtasks ...map[string]any) {
	if subtasks == nil {
		subtasks = []map[string]any{}
	}
	common.WriteJSON(w, 200, map[string]any{"flow_token": token, "status": "success", "subtasks": subtasks})
}

func nestedString(value any, key string) string {
	switch typed := value.(type) {
	case map[string]any:
		if direct, ok := typed[key].(string); ok {
			return direct
		}
		for _, child := range typed {
			if result := nestedString(child, key); result != "" {
				return result
			}
		}
	case []any:
		for _, child := range typed {
			if result := nestedString(child, key); result != "" {
				return result
			}
		}
	case []map[string]any:
		for _, child := range typed {
			if result := nestedString(child, key); result != "" {
				return result
			}
		}
	}
	return ""
}

func nestedEmail(value any) string {
	for _, key := range []string{"email", "text", "result"} {
		candidate := strings.ToLower(strings.TrimSpace(nestedString(value, key)))
		if strings.HasSuffix(candidate, "@flashlarry.test") {
			return candidate
		}
	}
	return ""
}

func clientIP(r *http.Request) string {
	value := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if net.ParseIP(value) != nil {
		return value
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return ip
	}
	return r.RemoteAddr
}

func (g *Gateway) mobileEmailCall(r *http.Request, route string, input map[string]string) (int, map[string]any, error) {
	body, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, g.usersURL+route, bytes.NewReader(body))
	if err != nil {
		return 500, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client-IP", clientIP(r))
	client := *g.client
	client.Timeout = 12 * time.Second
	response, err := client.Do(req)
	if err != nil {
		return 503, nil, err
	}
	defer response.Body.Close()
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return 502, nil, err
	}
	return response.StatusCode, result, nil
}

func (g *Gateway) mobileOnboarding(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var input struct {
		FlowName      string           `json:"flow_name"`
		FlowToken     string           `json:"flow_token"`
		SubtaskInputs []map[string]any `json:"subtask_inputs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		common.Error(w, 400, "invalid onboarding body")
		return
	}
	if input.FlowToken == "" {
		if input.FlowName != "login" {
			common.Error(w, 400, "unsupported onboarding flow")
			return
		}
		token, err := randomToken()
		if err != nil {
			common.Error(w, 500, "flow unavailable")
			return
		}
		if !g.storeFlow(token, mobileFlow{stage: 0, expires: time.Now().Add(10 * time.Minute)}) {
			common.Error(w, 429, "too many onboarding flows")
			return
		}
		mobileFlowResponse(w, token, mobileEmailEntrySubtask())
		return
	}
	flow, exists := g.loadFlow(input.FlowToken)
	if !exists {
		common.Error(w, 401, "onboarding flow expired")
		return
	}
	if flow.stage == 0 {
		email := nestedEmail(input.SubtaskInputs)
		if email == "" {
			common.Error(w, 400, "enter a @flashlarry.test email")
			return
		}
		status, result, err := g.mobileEmailCall(r, "/auth/email/start", map[string]string{"email": email})
		if err != nil {
			common.Error(w, status, "email challenge unavailable")
			return
		}
		if status != 200 {
			common.Error(w, status, fmt.Sprint(result["error"]))
			return
		}
		flow.email, flow.stage = email, 1
		if !g.storeFlow(input.FlowToken, flow) {
			common.Error(w, 429, "too many onboarding flows")
			return
		}
		mobileFlowResponse(w, input.FlowToken, mobileEmailSubtask())
		return
	}
	if flow.stage == 1 {
		code := nestedString(input.SubtaskInputs, "code")
		if code == "" {
			code = nestedString(input.SubtaskInputs, "text")
		}
		status, result, err := g.mobileEmailCall(r, "/auth/email/verify", map[string]string{"email": flow.email, "code": code})
		if err != nil {
			common.Error(w, status, "email verification unavailable")
			return
		}
		if status != 200 {
			common.Error(w, status, fmt.Sprint(result["error"]))
			return
		}
		token, _ := result["token"].(string)
		if len(token) != 64 {
			common.Error(w, 502, "invalid session from user service")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "auth_token", Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 7 * 24 * 3600})
		g.forgetFlow(input.FlowToken)
		mobileFlowResponse(w, input.FlowToken)
		return
	}
	common.Error(w, 400, "invalid onboarding stage")
}
