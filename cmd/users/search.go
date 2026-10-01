package main

import (
	"net/http"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
)

func (a *App) searchUsers(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(strings.TrimPrefix(r.URL.Query().Get("q"), "@"))
	if q == "" || len([]rune(q)) > 100 {
		common.Error(w, 400, "invalid search query")
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n < 1 {
		n = 20
	}
	if n > 100 {
		n = 100
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT id, screen_name, name, bio, location, website, avatar_url, verified, created_at
		FROM users WHERE strpos(lower(screen_name),lower($1)) > 0 OR strpos(lower(name),lower($1)) > 0
		ORDER BY CASE WHEN lower(screen_name)=lower($1) THEN 0 WHEN lower(screen_name) LIKE lower($1)||'%' THEN 1 ELSE 2 END, id DESC LIMIT $2`, q, n)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var user User
		var id int64
		if err := rows.Scan(&id, &user.ScreenName, &user.Name, &user.Bio, &user.Location, &user.Website, &user.AvatarURL, &user.Verified, &user.CreatedAt); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		user.ID = strconv.FormatInt(id, 10)
		users = append(users, user)
	}
	if rows.Err() != nil {
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, users)
}
