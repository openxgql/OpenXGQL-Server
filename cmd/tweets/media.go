package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
)

const maxMediaBytes = 100 << 20

type Media struct {
	ID   string `json:"id"`
	MIME string `json:"mime"`
	URL  string `json:"url"`
	Alt  string `json:"alt"`
}

func mediaDir() string { return common.Env("MEDIA_DIR", "/opt/flashlarry/media") }

func (a *App) initMedia() error {
	if err := os.MkdirAll(mediaDir(), 0750); err != nil {
		return err
	}
	_, err := a.db.Exec(`CREATE TABLE IF NOT EXISTS media (
		id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		owner_id BIGINT NOT NULL,
		mime VARCHAR(100) NOT NULL,
		expected_bytes BIGINT NOT NULL,
		status VARCHAR(12) NOT NULL DEFAULT 'uploading',
		alt_text VARCHAR(1000) NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`CREATE TABLE IF NOT EXISTS tweet_media (
		tweet_id BIGINT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE,
		media_id BIGINT NOT NULL REFERENCES media(id),
		position SMALLINT NOT NULL,
		PRIMARY KEY (tweet_id, media_id)
	)`)
	return err
}

func (a *App) uploadMedia(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-Flashlarry-User-ID")
	if !validID(userID) {
		common.Error(w, 401, "authentication required")
		return
	}
	command := strings.ToUpper(r.URL.Query().Get("command"))
	if r.Method == http.MethodPost && command == "INIT" {
		mime := r.URL.Query().Get("media_type")
		size, err := strconv.ParseInt(r.URL.Query().Get("total_bytes"), 10, 64)
		if err != nil || size < 1 || size > maxMediaBytes || !(mime == "image/jpeg" || mime == "image/png" || mime == "image/gif" || mime == "image/webp" || mime == "video/mp4" || mime == "video/webm" || mime == "video/quicktime") {
			common.Error(w, 400, "unsupported media type or size")
			return
		}
		var id int64
		if err := a.db.QueryRowContext(r.Context(), `INSERT INTO media (owner_id,mime,expected_bytes) VALUES ($1,$2,$3) RETURNING id`, userID, mime, size).Scan(&id); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		common.WriteJSON(w, 200, map[string]any{"media_id": id, "media_id_string": strconv.FormatInt(id, 10), "expires_after_secs": 3600})
		return
	}
	id := r.URL.Query().Get("media_id")
	if !validID(id) {
		common.Error(w, 400, "invalid media_id")
		return
	}
	var expected int64
	var status string
	err := a.db.QueryRowContext(r.Context(), `SELECT expected_bytes,status FROM media WHERE id=$1 AND owner_id=$2`, id, userID).Scan(&expected, &status)
	if err == sql.ErrNoRows {
		common.Error(w, 404, "media not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	if command == "APPEND" && r.Method == http.MethodPost {
		if status != "uploading" {
			common.Error(w, 409, "media already finalized")
			return
		}
		index, err := strconv.Atoi(r.URL.Query().Get("segment_index"))
		if err != nil || index < 0 || index > 999 {
			common.Error(w, 400, "invalid segment_index")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 8<<20)
		file, _, err := r.FormFile("media")
		if err != nil {
			common.Error(w, 400, "media part required")
			return
		}
		defer file.Close()
		dir := filepath.Join(mediaDir(), id+".parts")
		if err := os.MkdirAll(dir, 0750); err != nil {
			common.Error(w, 500, "storage error")
			return
		}
		path := filepath.Join(dir, fmt.Sprintf("%04d", index))
		out, err := os.Create(path)
		if err != nil {
			common.Error(w, 500, "storage error")
			return
		}
		n, copyErr := io.Copy(out, io.LimitReader(file, 8<<20))
		closeErr := out.Close()
		if copyErr != nil || closeErr != nil || n == 0 {
			_ = os.Remove(path)
			common.Error(w, 400, "invalid media segment")
			return
		}
		common.WriteJSON(w, 200, map[string]any{})
		return
	}
	if command == "FINALIZE" && r.Method == http.MethodPost {
		if status == "ready" {
			common.WriteJSON(w, 200, map[string]any{"media_id_string": id, "expires_after_secs": 3600})
			return
		}
		dir := filepath.Join(mediaDir(), id+".parts")
		files, err := os.ReadDir(dir)
		if err != nil || len(files) == 0 {
			common.Error(w, 400, "media segments missing")
			return
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		tmp := filepath.Join(mediaDir(), id+".tmp")
		out, err := os.Create(tmp)
		if err != nil {
			common.Error(w, 500, "storage error")
			return
		}
		var total int64
		for i, entry := range files {
			if entry.Name() != fmt.Sprintf("%04d", i) {
				_ = out.Close()
				_ = os.Remove(tmp)
				common.Error(w, 400, "media segments missing")
				return
			}
			part, err := os.Open(filepath.Join(dir, entry.Name()))
			if err != nil {
				_ = out.Close()
				_ = os.Remove(tmp)
				common.Error(w, 500, "storage error")
				return
			}
			n, err := io.Copy(out, part)
			_ = part.Close()
			if err != nil {
				_ = out.Close()
				_ = os.Remove(tmp)
				common.Error(w, 500, "storage error")
				return
			}
			total += n
		}
		if err := out.Close(); err != nil || total != expected {
			_ = os.Remove(tmp)
			common.Error(w, 400, "media size mismatch")
			return
		}
		if err := os.Rename(tmp, filepath.Join(mediaDir(), id)); err != nil {
			common.Error(w, 500, "storage error")
			return
		}
		if _, err := a.db.ExecContext(r.Context(), `UPDATE media SET status='ready' WHERE id=$1 AND owner_id=$2`, id, userID); err != nil {
			common.Error(w, 500, "database error")
			return
		}
		_ = os.RemoveAll(dir)
		common.WriteJSON(w, 200, map[string]any{"media_id_string": id, "expires_after_secs": 3600})
		return
	}
	if command == "STATUS" && r.Method == http.MethodGet {
		if status == "ready" {
			common.WriteJSON(w, 200, map[string]any{"expires_after_secs": 3600})
			return
		}
		common.WriteJSON(w, 200, map[string]any{"expires_after_secs": 3600, "processing_info": map[string]any{"state": "pending", "check_after_secs": 1}})
		return
	}
	common.Error(w, 400, "unsupported media command")
}

func (a *App) mediaMetadata(w http.ResponseWriter, r *http.Request) {
	userID := r.Header.Get("X-Flashlarry-User-ID")
	if !validID(userID) {
		common.Error(w, 401, "authentication required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var input struct {
		MediaID string `json:"media_id"`
		AltText struct {
			Text string `json:"text"`
		} `json:"alt_text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || !validID(input.MediaID) || len([]rune(input.AltText.Text)) > 1000 {
		common.Error(w, 400, "invalid media metadata")
		return
	}
	result, err := a.db.ExecContext(r.Context(), `UPDATE media SET alt_text=$3 WHERE id=$1 AND owner_id=$2`, input.MediaID, userID, input.AltText.Text)
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		common.Error(w, 404, "media not found")
		return
	}
	common.WriteJSON(w, 200, map[string]any{})
}

func (a *App) loadMedia(ctx context.Context, tweet *Tweet) error {
	rows, err := a.db.QueryContext(ctx, `SELECT m.id,m.mime,m.alt_text FROM tweet_media tm JOIN media m ON m.id=tm.media_id WHERE tm.tweet_id=$1 ORDER BY tm.position`, tweet.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var media Media
		if err := rows.Scan(&media.ID, &media.MIME, &media.Alt); err != nil {
			return err
		}
		media.URL = common.PublicBaseURL() + "/media/" + media.ID
		tweet.Media = append(tweet.Media, media)
	}
	return rows.Err()
}

func (a *App) serveMedia(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validID(id) {
		common.Error(w, 404, "media not found")
		return
	}
	var mime string
	if err := a.db.QueryRowContext(r.Context(), `SELECT mime FROM media WHERE id=$1 AND status='ready'`, id).Scan(&mime); err != nil {
		common.Error(w, 404, "media not found")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(mediaDir(), id))
}

func (a *App) avatarMediaInfo(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ownerID := r.URL.Query().Get("owner_id")
	if !validID(id) || !validID(ownerID) {
		common.Error(w, 400, "invalid media or owner id")
		return
	}
	var mime string
	var size int64
	err := a.db.QueryRowContext(r.Context(), `SELECT mime,expected_bytes FROM media WHERE id=$1 AND owner_id=$2 AND status='ready'`, id, ownerID).Scan(&mime, &size)
	if err == sql.ErrNoRows {
		common.Error(w, 404, "ready media not found")
		return
	}
	if err != nil {
		common.Error(w, 500, "database error")
		return
	}
	if size > 10<<20 || !(mime == "image/jpeg" || mime == "image/png" || mime == "image/gif" || mime == "image/webp") {
		common.Error(w, 400, "avatar must be an image up to 10 MiB")
		return
	}
	file, err := os.Open(filepath.Join(mediaDir(), id))
	if err != nil {
		common.Error(w, 404, "media file not found")
		return
	}
	defer file.Close()
	var header [512]byte
	n, err := file.Read(header[:])
	if err != nil && err != io.EOF {
		common.Error(w, 500, "media file unavailable")
		return
	}
	if http.DetectContentType(header[:n]) != mime {
		common.Error(w, 400, "media content does not match image type")
		return
	}
	common.WriteJSON(w, 200, map[string]string{"url": common.PublicBaseURL() + "/media/" + id, "mime": mime})
}

func (a *App) videos(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if !validID(userID) {
		common.Error(w, 400, "invalid user_id")
		return
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT DISTINCT t.id,t.author_id,t.text,t.reply_to_id,t.created_at
		FROM tweets t JOIN tweet_media tm ON tm.tweet_id=t.id JOIN media m ON m.id=tm.media_id
		WHERE t.author_id=$1 AND m.mime LIKE 'video/%' ORDER BY t.id DESC LIMIT $2`, userID, limit(r))
	a.writeRows(r.Context(), w, rows, err)
}
