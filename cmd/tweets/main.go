package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/common"
	"github.com/redis/go-redis/v9"
)

type Tweet struct {
	ID        string    `json:"id"`
	AuthorID  string    `json:"author_id"`
	Text      string    `json:"text"`
	ReplyToID *string   `json:"reply_to_id"`
	CreatedAt time.Time `json:"created_at"`
	Media     []Media   `json:"media,omitempty"`
}

type App struct {
	db    *sql.DB
	cache *redis.Client
}

func main() {
	app := &App{db: common.OpenDB(), cache: common.OpenRedis()}
	_, err := app.db.Exec(`CREATE TABLE IF NOT EXISTS tweets (
		id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		author_id BIGINT NOT NULL,
		text VARCHAR(280) NOT NULL,
		reply_to_id BIGINT REFERENCES tweets(id) ON DELETE SET NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`ALTER TABLE tweets ADD COLUMN IF NOT EXISTS reply_to_id BIGINT REFERENCES tweets(id) ON DELETE SET NULL`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`CREATE INDEX IF NOT EXISTS tweets_author_id_id_idx ON tweets (author_id, id DESC)`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`CREATE TABLE IF NOT EXISTS reactions (
		tweet_id BIGINT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE,
		user_id BIGINT NOT NULL,
		kind VARCHAR(10) NOT NULL CHECK (kind = 'like'),
		PRIMARY KEY (tweet_id, user_id)
	)`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`CREATE INDEX IF NOT EXISTS reactions_tweet_kind_idx ON reactions (tweet_id, kind)`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`CREATE INDEX IF NOT EXISTS tweets_reply_to_id_id_idx ON tweets (reply_to_id, id)`)
	if err != nil {
		panic(err)
	}
	_, err = app.db.Exec(`CREATE TABLE IF NOT EXISTS retweets (
		tweet_id BIGINT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE,
		user_id BIGINT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (tweet_id, user_id)
	)`)
	if err != nil {
		panic(err)
	}
	if _, err = app.db.Exec(`ALTER TABLE tweets ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT false;ALTER TABLE tweets ADD COLUMN IF NOT EXISTS is_ad BOOLEAN NOT NULL DEFAULT false`); err != nil {
		panic(err)
	}
	if err := app.initMedia(); err != nil {
		panic(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", app.health)
	mux.HandleFunc("POST /tweets", app.create)
	mux.HandleFunc("GET /tweets", app.list)
	mux.HandleFunc("GET /tweets/{id}", app.byID)
	mux.HandleFunc("DELETE /tweets/{id}", app.delete)
	mux.HandleFunc("GET /tweets/{id}/replies", app.replies)
	mux.HandleFunc("GET /timeline", app.timeline)
	mux.HandleFunc("GET /search", app.searchTweets)
	mux.HandleFunc("PUT /tweets/{id}/reaction", app.putReaction)
	mux.HandleFunc("DELETE /tweets/{id}/reaction", app.deleteReaction)
	mux.HandleFunc("GET /tweets/{id}/reactions", app.reactions)
	mux.HandleFunc("GET /likes", app.likes)
	mux.HandleFunc("GET /reposts", app.reposts)
	mux.HandleFunc("GET /videos", app.videos)
	mux.HandleFunc("PUT /tweets/{id}/retweet", app.putRetweet)
	mux.HandleFunc("DELETE /tweets/{id}/retweet", app.deleteRetweet)
	mux.HandleFunc("POST /media/upload", app.uploadMedia)
	mux.HandleFunc("GET /media/upload", app.uploadMedia)
	mux.HandleFunc("POST /media/metadata", app.mediaMetadata)
	mux.HandleFunc("GET /media/{id}", app.serveMedia)
	mux.HandleFunc("GET /media/{id}/avatar-info", app.avatarMediaInfo)
	common.Serve(common.Env("LISTEN_ADDR", "127.0.0.1:8082"), mux)
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if a.db.PingContext(ctx) != nil || a.cache.Ping(ctx).Err() != nil {
		common.Error(w, 503, "dependency unavailable")
		return
	}
	common.WriteJSON(w, 200, map[string]string{"status": "ok"})
}

func (a *App) create(w http.ResponseWriter, r *http.Request) {
	var input struct {
		AuthorID  string   `json:"author_id"`
		Text      string   `json:"text"`
		ReplyToID *string  `json:"reply_to_id"`
		MediaIDs  []string `json:"media_ids"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	input.Text = strings.TrimSpace(input.Text)
	if _, err := strconv.ParseInt(input.AuthorID, 10, 64); err != nil || (input.Text == "" && len(input.MediaIDs) == 0) || len([]rune(input.Text)) > 280 || len(input.MediaIDs) > 4 {
		common.Error(w, 400, "invalid author_id, text or media count")
		return
	}
	for _, mediaID := range input.MediaIDs {
		if !validID(mediaID) {
			common.Error(w, 400, "invalid media_id")
			return
		}
	}
	var replyTo any
	if input.ReplyToID != nil {
		if !validID(*input.ReplyToID) {
			common.Error(w, 400, "invalid reply_to_id")
			return
		}
		replyTo = *input.ReplyToID
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	defer tx.Rollback()
	var id int64
	var created time.Time
	err = tx.QueryRowContext(r.Context(), `INSERT INTO tweets (author_id, text, reply_to_id)
		SELECT $1, $2, $3 WHERE $3::bigint IS NULL OR EXISTS (SELECT 1 FROM tweets WHERE id=$3)
		RETURNING id, created_at`, input.AuthorID, input.Text, replyTo).Scan(&id, &created)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "parent tweet not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	for position, mediaID := range input.MediaIDs {
		result, err := tx.ExecContext(r.Context(), `INSERT INTO tweet_media (tweet_id,media_id,position)
			SELECT $1,id,$3 FROM media WHERE id=$2 AND owner_id=$4 AND status='ready'`, id, mediaID, position, input.AuthorID)
		if err != nil {
			common.Error(w, 500, "database error")
			return
		}
		n, _ := result.RowsAffected()
		if n != 1 {
			common.Error(w, 400, "media is not ready or not owned by author")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	tweet := Tweet{ID: strconv.FormatInt(id, 10), AuthorID: input.AuthorID, Text: input.Text, ReplyToID: input.ReplyToID, CreatedAt: created}
	if err := a.loadMedia(r.Context(), &tweet); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	a.cacheTweet(r.Context(), tweet)
	common.WriteJSON(w, 201, tweet)
}

func (a *App) byID(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		common.Error(w, 400, "invalid tweet id")
		return
	}
	if cached, err := a.cache.Get(r.Context(), "tweet:"+id).Bytes(); err == nil {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Cache", "HIT")
		_, _ = w.Write(append(cached, '\n'))
		return
	}
	var tweet Tweet
	var numericID int64
	var replyTo sql.NullInt64
	err := a.db.QueryRowContext(r.Context(), `SELECT id, author_id, text, reply_to_id, created_at FROM tweets WHERE id=$1`, id).Scan(&numericID, &tweet.AuthorID, &tweet.Text, &replyTo, &tweet.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "tweet not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	tweet.ID = strconv.FormatInt(numericID, 10)
	if replyTo.Valid {
		value := strconv.FormatInt(replyTo.Int64, 10)
		tweet.ReplyToID = &value
	}
	if err := a.loadMedia(r.Context(), &tweet); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	a.cacheTweet(r.Context(), tweet)
	common.WriteJSON(w, 200, tweet)
}

func (a *App) delete(w http.ResponseWriter, r *http.Request) {
	id, idErr := strconv.ParseInt(r.PathValue("id"), 10, 64)
	authorID, authorErr := strconv.ParseInt(r.URL.Query().Get("author_id"), 10, 64)
	if idErr != nil || authorErr != nil {
		common.Error(w, 400, "invalid id or author_id")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `DELETE FROM tweets WHERE id=$1 AND author_id=$2`, id, authorID)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		common.Error(w, 404, "tweet not found for author")
		return
	}
	_ = a.cache.Del(r.Context(), "tweet:"+strconv.FormatInt(id, 10)).Err()
	common.WriteJSON(w, 200, map[string]any{"deleted": true, "id": strconv.FormatInt(id, 10)})
}

func (a *App) list(w http.ResponseWriter, r *http.Request) {
	authorID := r.URL.Query().Get("author_id")
	if _, err := strconv.ParseInt(authorID, 10, 64); err != nil {
		common.Error(w, 400, "invalid author_id")
		return
	}
	filter := ""
	switch r.URL.Query().Get("mode") {
	case "replies":
		filter = " AND reply_to_id IS NOT NULL"
	case "all":
	default:
		filter = " AND reply_to_id IS NULL"
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, author_id, text, reply_to_id, created_at FROM tweets WHERE NOT hidden AND NOT is_ad AND author_id=$1`+filter+` ORDER BY id DESC LIMIT $2`, authorID, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}

func (a *App) timeline(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, author_id, text, reply_to_id, created_at FROM tweets WHERE NOT hidden AND NOT is_ad ORDER BY id DESC LIMIT $1`, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}

func (a *App) replies(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		common.Error(w, 400, "invalid tweet id")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, author_id, text, reply_to_id, created_at FROM tweets WHERE NOT hidden AND NOT is_ad AND reply_to_id=$1 ORDER BY id ASC LIMIT $2`, id, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}

func limit(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		return 20
	}
	if n > 100 {
		return 100
	}
	return n
}

func (a *App) writeRows(ctx context.Context, w http.ResponseWriter, rows *sql.Rows, err error) {
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	defer rows.Close()
	tweets := make([]Tweet, 0)
	for rows.Next() {
		var tweet Tweet
		var id int64
		var replyTo sql.NullInt64
		if err := rows.Scan(&id, &tweet.AuthorID, &tweet.Text, &replyTo, &tweet.CreatedAt); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		tweet.ID = strconv.FormatInt(id, 10)
		if replyTo.Valid {
			value := strconv.FormatInt(replyTo.Int64, 10)
			tweet.ReplyToID = &value
		}
		tweets = append(tweets, tweet)
	}
	if rows.Err() != nil {
		common.Error(w, 500, "database error")
		return
	}
	for i := range tweets {
		if err := a.loadMedia(ctx, &tweets[i]); err != nil {
			common.Error(w, 500, "database error")
			return
		}
	}
	common.WriteJSON(w, 200, tweets)
}

func (a *App) cacheTweet(ctx context.Context, tweet Tweet) {
	value, err := json.Marshal(tweet)
	if err == nil {
		_ = a.cache.Set(ctx, "tweet:"+tweet.ID, value, time.Minute).Err()
	}
}
