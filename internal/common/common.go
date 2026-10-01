package common

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"
)

func Env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func PublicBaseURL() string {
	return strings.TrimRight(Env("PUBLIC_BASE_URL", "https://flashlarry.duckdns.org"), "/")
}

func OpenDB() *sql.DB {
	db, err := sql.Open("pgx", Env("DATABASE_URL", "postgres:///flashlarry?host=/var/run/postgresql&sslmode=disable"))
	if err != nil {
		log.Fatal(err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatal(err)
	}
	return db
}

func OpenRedis() *redis.Client {
	client := redis.NewClient(&redis.Options{Addr: Env("REDIS_ADDR", "127.0.0.1:6379"), PoolSize: 20})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		log.Fatal(err)
	}
	return client
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func Error(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]any{"error": message})
}

func Decode(w http.ResponseWriter, r *http.Request, value any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		Error(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		Error(w, http.StatusBadRequest, "only one JSON value is allowed")
		return false
	}
	return true
}

func Serve(address string, handler http.Handler) {
	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}
	log.Printf("listening on %s", address)
	log.Fatal(server.ListenAndServe())
}
