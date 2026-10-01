package main

import (
	"net/http"
	"strings"

	"flashlarry-api/internal/common"
)

func (a *App) searchTweets(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || len([]rune(q)) > 200 {
		common.Error(w, 400, "invalid search query")
		return
	}
	mediaFilter := ""
	switch strings.ToLower(r.URL.Query().Get("product")) {
	case "photos", "images":
		mediaFilter = " AND EXISTS (SELECT 1 FROM tweet_media tm JOIN media m ON m.id=tm.media_id WHERE tm.tweet_id=t.id AND m.mime LIKE 'image/%')"
	case "videos":
		mediaFilter = " AND EXISTS (SELECT 1 FROM tweet_media tm JOIN media m ON m.id=tm.media_id WHERE tm.tweet_id=t.id AND m.mime LIKE 'video/%')"
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT t.id,t.author_id,t.text,t.reply_to_id,t.created_at FROM tweets t
		WHERE strpos(lower(t.text),lower($1)) > 0`+mediaFilter+` ORDER BY t.id DESC LIMIT $2`, q, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}
