package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"flashlarry-api/internal/common"
)

type Relationship struct {
	Followers    int64 `json:"followers"`
	Following    int64 `json:"following"`
	IsFollowing  bool  `json:"is_following"`
	IsFollowedBy bool  `json:"is_followed_by"`
}

type FollowedUser struct {
	FollowID string `json:"follow_id"`
	User     User   `json:"user"`
}

type FollowList struct {
	Users      []FollowedUser `json:"users"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

func (a *App) initFollows() error {
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS follows (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			follower_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			target_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT follows_no_self CHECK (follower_id <> target_id),
			CONSTRAINT follows_pair_unique UNIQUE (follower_id,target_id)
		)`,
		`CREATE INDEX IF NOT EXISTS follows_target_id_id_idx ON follows (target_id,id DESC)`,
		`CREATE INDEX IF NOT EXISTS follows_follower_id_id_idx ON follows (follower_id,id DESC)`,
	} {
		if _, err := a.db.Exec(query); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) follow(w http.ResponseWriter, r *http.Request) {
	targetID := r.PathValue("id")
	followerID := r.PathValue("followerID")
	if _, err := strconv.ParseInt(targetID, 10, 64); err != nil {
		common.Error(w, 400, "invalid target user id")
		return
	}
	if _, err := strconv.ParseInt(followerID, 10, 64); err != nil || targetID == followerID {
		common.Error(w, 400, "invalid follower id")
		return
	}
	if r.Method == http.MethodPut {
		var exists bool
		if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, targetID).Scan(&exists); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		if !exists {
			common.Error(w, 404, "user not found")
			return
		}
		if _, err := a.db.ExecContext(r.Context(), `INSERT INTO follows (follower_id,target_id) VALUES ($1,$2) ON CONFLICT (follower_id,target_id) DO NOTHING`, followerID, targetID); err != nil {
			common.Error(w, 400, "could not follow user")
			return
		}
		common.WriteJSON(w, 200, map[string]bool{"following": true})
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM follows WHERE follower_id=$1 AND target_id=$2`, followerID, targetID); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, map[string]bool{"following": false})
}

func (a *App) relationship(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		common.Error(w, 400, "invalid user id")
		return
	}
	viewerID := r.URL.Query().Get("viewer_id")
	if viewerID == "" {
		viewerID = "0"
	}
	if viewerID != "0" {
		if _, err := strconv.ParseInt(viewerID, 10, 64); err != nil {
			common.Error(w, 400, "invalid viewer id")
			return
		}
	}
	var data Relationship
	err := a.db.QueryRowContext(r.Context(), `SELECT
		(SELECT count(*) FROM follows WHERE target_id=$1),
		(SELECT count(*) FROM follows WHERE follower_id=$1),
		EXISTS(SELECT 1 FROM follows WHERE target_id=$1 AND follower_id=$2),
		EXISTS(SELECT 1 FROM follows WHERE follower_id=$1 AND target_id=$2)
		FROM users WHERE id=$1`, id, viewerID).
		Scan(&data.Followers, &data.Following, &data.IsFollowing, &data.IsFollowedBy)
	if errors.Is(err, sql.ErrNoRows) {
		common.Error(w, 404, "user not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	common.WriteJSON(w, 200, data)
}

func (a *App) followList(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("kind") != "followers" && r.PathValue("kind") != "following" {
		common.Error(w, 404, "follow list not found")
		return
	}
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		common.Error(w, 400, "invalid user id")
		return
	}
	var exists bool
	if err := a.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1)`, id).Scan(&exists); err != nil {
		common.Error(w, 500, "database error")
		return
	}
	if !exists {
		common.Error(w, 404, "user not found")
		return
	}
	cursor := int64(0)
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		var err error
		cursor, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || cursor <= 0 {
			common.Error(w, 400, "invalid cursor")
			return
		}
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 100 {
		limit = 100
	}
	query := `SELECT f.id,u.id,u.screen_name,u.name,u.bio,u.location,u.website,u.avatar_url,u.verified,u.created_at
		FROM follows f JOIN users u ON u.id=f.follower_id
		WHERE f.target_id=$1 AND ($2::bigint=0 OR f.id<$2) ORDER BY f.id DESC LIMIT $3`
	if r.PathValue("kind") == "following" {
		query = `SELECT f.id,u.id,u.screen_name,u.name,u.bio,u.location,u.website,u.avatar_url,u.verified,u.created_at
			FROM follows f JOIN users u ON u.id=f.target_id
			WHERE f.follower_id=$1 AND ($2::bigint=0 OR f.id<$2) ORDER BY f.id DESC LIMIT $3`
	}
	rows, err := a.db.QueryContext(r.Context(), query, id, cursor, limit+1)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	defer rows.Close()
	result := FollowList{Users: make([]FollowedUser, 0)}
	for rows.Next() {
		var item FollowedUser
		var followID, userID int64
		if err := rows.Scan(&followID, &userID, &item.User.ScreenName, &item.User.Name, &item.User.Bio, &item.User.Location, &item.User.Website, &item.User.AvatarURL, &item.User.Verified, &item.User.CreatedAt); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		item.FollowID = strconv.FormatInt(followID, 10)
		item.User.ID = strconv.FormatInt(userID, 10)
		result.Users = append(result.Users, item)
	}
	if rows.Err() != nil {
		common.Error(w, 500, "database error")
		return
	}
	if len(result.Users) > limit {
		result.Users = result.Users[:limit]
		result.NextCursor = result.Users[len(result.Users)-1].FollowID
	}
	common.WriteJSON(w, 200, result)
}
