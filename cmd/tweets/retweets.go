package main

import (
	"errors"
	"net/http"

	"flashlarry-api/internal/common"
	"github.com/jackc/pgx/v5/pgconn"
)

func (a *App) putRetweet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var input struct {
		UserID string `json:"user_id"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	if !validID(id) || !validID(input.UserID) {
		common.Error(w, 400, "invalid tweet_id or user_id")
		return
	}
	_, err := a.db.ExecContext(r.Context(), `INSERT INTO retweets (tweet_id,user_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, input.UserID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			common.Error(w, 404, "tweet not found")
			return
		}
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, map[string]any{"retweeted": true, "tweet_id": id})
}

func (a *App) deleteRetweet(w http.ResponseWriter, r *http.Request) {
	id, userID := r.PathValue("id"), r.URL.Query().Get("user_id")
	if !validID(id) || !validID(userID) {
		common.Error(w, 400, "invalid tweet_id or user_id")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM retweets WHERE tweet_id=$1 AND user_id=$2`, id, userID); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, map[string]any{"retweeted": false, "tweet_id": id})
}

func (a *App) reposts(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if !validID(userID) {
		common.Error(w, 400, "invalid user_id")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT t.id,t.author_id,t.text,t.reply_to_id,t.created_at
		FROM retweets rt JOIN tweets t ON t.id=rt.tweet_id
		WHERE rt.user_id=$1 ORDER BY rt.created_at DESC LIMIT $2`, userID, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}
