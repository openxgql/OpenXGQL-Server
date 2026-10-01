package main

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"time"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/social"
)

func main() {
	s := &social.Store{DB: common.OpenDB(), Cache: common.OpenRedis()}
	if e := s.Init(logContext()); e != nil {
		log.Fatal(e)
	}
	secret := common.Env("SOCIAL_INTERNAL_TOKEN", "")
	if len(secret) < 32 {
		log.Fatal("SOCIAL_INTERNAL_TOKEN required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /feed",func(w http.ResponseWriter,r *http.Request){var req social.FeedRequest;if !common.Decode(w,r,&req){return};res,e:=s.Feed(r.Context(),req);if e!=nil{common.Error(w,social.ErrorStatus(e),social.ErrorMessage(e));return};common.WriteJSON(w,200,res)})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		common.WriteJSON(w, 200, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /execute", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			Name      string         `json:"name"`
			ID        string         `json:"id"`
			UserID    string         `json:"user_id"`
			Variables map[string]any `json:"variables"`
		}
		if !common.Decode(w, r, &v) {
			return
		}
		spec, ok := social.Lookup(v.Name, v.ID)
		if !ok {
			common.Error(w, 404, "unknown operation or query ID")
			return
		}
		res, e := s.Execute(r.Context(), spec, v.UserID, v.Variables)
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 200, res)
	})
	mux.HandleFunc("POST /visibility", func(w http.ResponseWriter, r *http.Request) {
		var v struct {
			UserID string   `json:"user_id"`
			IDs    []string `json:"ids"`
		}
		if !common.Decode(w, r, &v) {
			return
		}
		ids, e := s.VisiblePosts(r.Context(), v.UserID, v.IDs)
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 200, ids)
	})
	mux.HandleFunc("GET /users/{id}/state", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.UserState(r.Context(), r.PathValue("id"), r.URL.Query().Get("viewer_id"))
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 200, v)
	})
	mux.HandleFunc("GET /posts/{id}/meta", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.PostMeta(r.Context(), r.PathValue("id"), r.URL.Query().Get("viewer_id"))
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 200, v)
	})
	for _, action := range []string{"validate", "commit"} {
		mux.HandleFunc("POST /posts/"+action, func(w http.ResponseWriter, r *http.Request) {
			var v struct {
				UserID    string         `json:"user_id"`
				ID        string         `json:"id"`
				Variables map[string]any `json:"variables"`
			}
			if !common.Decode(w, r, &v) {
				return
			}
			var e error
			if action == "validate" {
				e = s.ValidatePost(r.Context(), v.UserID, v.Variables)
			} else {
				e = s.CommitPost(r.Context(), v.UserID, v.ID, v.Variables)
			}
			if e != nil {
				common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
				return
			}
			common.WriteJSON(w, 200, map[string]any{"success": true})
		})
	}
	mux.HandleFunc("POST /reports", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]any
		if !common.Decode(w, r, &v) {
			return
		}
		res, e := s.Report(r.Context(), social.Str(v, "reporter_id"), v)
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 201, res)
	})
	mux.HandleFunc("GET /ads/next", func(w http.ResponseWriter, r *http.Request) {
		v, e := s.NextAd(r.Context(), r.URL.Query().Get("viewer_id"))
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 200, v)
	})
	mux.HandleFunc("GET /ads/click/{delivery}", func(w http.ResponseWriter, r *http.Request) {
		target, e := s.AdClick(r.Context(), r.PathValue("delivery"))
		if e != nil {
			common.Error(w, social.ErrorStatus(e), social.ErrorMessage(e))
			return
		}
		common.WriteJSON(w, 200, map[string]string{"target_url": target})
	})
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			if e := s.PublishScheduled(logContext()); e != nil {
				log.Printf("scheduler: %v", e)
			}
		}
	}()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Token")), []byte(secret)) != 1 {
			common.Error(w, 403, "internal service authentication required")
			return
		}
		mux.ServeHTTP(w, r)
	})
	common.Serve(common.Env("LISTEN_ADDR", "127.0.0.1:8083"), handler)
}

func logContext() context.Context { return context.Background() }
