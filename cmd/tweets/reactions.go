package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"flashlarry-api/internal/common"
	"github.com/jackc/pgx/v5/pgconn"
)

func validID(id string) bool {
	value, err := strconv.ParseInt(id, 10, 64)
	return err == nil && value > 0
}

func (a *App) putReaction(w http.ResponseWriter, r *http.Request) {
	tweetID := r.PathValue("id")
	var input struct {
		UserID string `json:"user_id"`
	}
	if !common.Decode(w, r, &input) {
		return
	}
	if !validID(tweetID) || !validID(input.UserID) {
		common.Error(w, 400, "invalid tweet_id or user_id")
		return
	}
	_, err := a.db.ExecContext(r.Context(), `INSERT INTO reactions (tweet_id, user_id, kind) VALUES ($1,$2,'like')
		ON CONFLICT (tweet_id,user_id) DO NOTHING`, tweetID, input.UserID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			common.Error(w, 404, "tweet not found")
			return
		}
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, map[string]bool{"favorited": true})
}

func (a *App) deleteReaction(w http.ResponseWriter, r *http.Request) {
	tweetID := r.PathValue("id")
	userID := r.URL.Query().Get("user_id")
	if !validID(tweetID) || !validID(userID) {
		common.Error(w, 400, "invalid tweet_id or user_id")
		return
	}
	_, err := a.db.ExecContext(r.Context(), `DELETE FROM reactions WHERE tweet_id=$1 AND user_id=$2`, tweetID, userID)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, map[string]bool{"favorited": false})
}

func (a *App) reactions(w http.ResponseWriter, r *http.Request) {
	tweetID := r.PathValue("id")
	if !validID(tweetID) {
		common.Error(w, 400, "invalid tweet_id")
		return
	}
	var favorites, replies, retweets int64
	err := a.db.QueryRowContext(r.Context(), `SELECT
		(SELECT count(*) FROM reactions WHERE tweet_id=$1),
		(SELECT count(*) FROM tweets WHERE reply_to_id=$1),
		(SELECT count(*) FROM retweets WHERE tweet_id=$1)
		WHERE EXISTS (SELECT 1 FROM tweets WHERE id=$1)`, tweetID).Scan(&favorites, &replies, &retweets)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "tweet not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	var favorited, retweeted bool
	if userID := r.URL.Query().Get("user_id"); validID(userID) {
		if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM reactions WHERE tweet_id=$1 AND user_id=$2)`, tweetID, userID).Scan(&favorited); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM retweets WHERE tweet_id=$1 AND user_id=$2)`, tweetID, userID).Scan(&retweeted); err != nil {
			common.Error(w, 500, "database error")
			return
		}
	}
	common.WriteJSON(w, 200, map[string]any{"favorite_count": favorites, "reply_count": replies, "retweet_count": retweets, "favorited": favorited, "retweeted": retweeted})
}

func (a *App) likes(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if !validID(userID) {
		common.Error(w, 400, "invalid user_id")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT t.id, t.author_id, t.text, t.reply_to_id, t.created_at
		FROM reactions AS r JOIN tweets AS t ON t.id=r.tweet_id
		WHERE r.user_id=$1 ORDER BY t.id DESC LIMIT $2`, userID, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}
