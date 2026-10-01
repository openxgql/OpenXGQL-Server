package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

func (s *Store) articles(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	id := Str(v, "articleEntityId", "article_entity_id", "articleId", "article_id", "restId", "rest_id", "id")
	switch a {
	case "ArticleEntityDraftCreate":
		if Str(v, "title") == "" {
			v["title"] = ""
		}
		v["state"] = "draft"
		value, e := s.createEntity(ctx, user, "article", "", v, false)
		return Result{Value: value}, e
	case "ArticleEntityDelete", "ArticleEntityUpdateContent", "ArticleEntityUpdateTitle", "ArticleEntityUpdateCoverMedia":
		if len([]rune(Str(v, "title"))) > 200 {
			return Result{}, fail(400, "title too long")
		}
		value, e := s.updateEntity(ctx, user, "article", id, v, a == "ArticleEntityDelete")
		return Result{Value: value}, e
	case "ArticleEntityPublish", "ArticleEntityUnpublish":
		value, e := s.getEntity(ctx, user, "article", id, false)
		if e != nil {
			return Result{}, e
		}
		published := a == "ArticleEntityPublish"
		state := "draft"
		if published {
			state = "published"
			if Str(value, "title") == "" {
				return Result{}, fail(400, "article title is required to publish")
			}
		}
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return Result{}, e
		}
		defer tx.Rollback()
		update := map[string]any{"state": state}
		tweet := Str(value, "tweet_id")
		if published && tweet == "" {
			title := []rune(Str(value, "title"))
			if len(title) > 280 {
				title = title[:280]
			}
			var postID int64
			if e = tx.QueryRowContext(ctx, `INSERT INTO tweets(author_id,text) VALUES($1,$2) RETURNING id`, user, string(title)).Scan(&postID); e != nil {
				return Result{}, e
			}
			tweet = strconv.FormatInt(postID, 10)
			update["tweet_id"] = tweet
		}
		if _, e = tx.ExecContext(ctx, `UPDATE fl_entities SET data=data||$1,public=$2,updated_at=now() WHERE id::text=$3 AND owner_id=$4::bigint AND kind='article'`, Encode(update), published, id, user); e != nil {
			return Result{}, e
		}
		if e = tx.Commit(); e != nil {
			return Result{}, e
		}
		value, e = s.getEntity(ctx, user, "article", id, false)
		return Result{Value: value}, e
	case "ArticleEntityResultByRestId":
		value, e := s.getEntity(ctx, user, "article", id, true)
		return Result{Value: value}, e
	case "ArticleByTweetId":
		tweet, e := require(v, "tweetId", "tweet_id", "restId", "rest_id")
		if e != nil {
			return Result{}, e
		}
		if e = s.DB.QueryRowContext(ctx, `SELECT id::text FROM fl_entities WHERE kind='article' AND data->>'tweet_id'=$1 AND deleted_at IS NULL AND (owner_id=$2::bigint OR public)`, tweet, user).Scan(&id); e == sql.ErrNoRows {
			return Result{}, fail(404, "article not found")
		} else if e != nil {
			return Result{}, e
		}
		value, e := s.getEntity(ctx, user, "article", id, true)
		return Result{Value: value}, e
	default:
		items, e := s.listEntities(ctx, user, "article", "", Str(v, "query"), false)
		return Result{Value: map[string]any{"items": items, "items_results": items, "entities": items}}, e
	}
}
func (s *Store) notes(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	id := Str(v, "noteId", "note_id", "restId", "rest_id", "id")
	tweet := Str(v, "tweetId", "tweet_id", "postId", "post_id")
	switch a {
	case "BirdwatchCreateNote":
		if tweet == "" {
			return Result{}, fail(400, "tweet_id is required")
		}
		if e := s.exists(ctx, "tweets", tweet); e != nil {
			return Result{}, e
		}
		text := Str(v, "summary", "text", "note_text")
		if strings.TrimSpace(text) == "" || len([]rune(text)) > 4000 {
			return Result{}, fail(400, "note summary must contain 1-4000 characters")
		}
		v["summary"] = text
		v["tweet_id"] = tweet
		value, e := s.createEntity(ctx, user, "note", tweet, v, true)
		return Result{Value: value}, e
	case "BirdwatchDeleteNote":
		value, e := s.updateEntity(ctx, user, "note", id, v, true)
		return Result{Value: value, Scalar: "Done"}, e
	case "BirdwatchCreateRating", "BirdwatchDeleteRating":
		if _, e := s.getEntity(ctx, user, "note", id, true); e != nil {
			return Result{}, e
		}
		var e error
		if a == "BirdwatchCreateRating" {
			level := Str(v, "helpfulnessLevel", "helpfulness_level", "rating")
			if level == "" && v["helpful"] == nil {
				return Result{}, fail(400, "rating is required")
			}
			e = s.putRelation(ctx, user, "note_rating", id, v)
		} else {
			e = s.deleteRelation(ctx, user, "note_rating", id)
		}
		value, err := s.getEntity(ctx, user, "note", id, true)
		if e != nil {
			return Result{}, e
		}
		s.noteCounts(ctx, id, value)
		return Result{Value: value, Scalar: "Done"}, err
	case "BirdwatchCreateAppeal", "BirdwatchCreateBatSignal":
		kind := "note_appeal"
		if a == "BirdwatchCreateBatSignal" {
			kind = "note_signal"
		}
		target := tweet
		if target == "" {
			target = id
		}
		if target == "" {
			return Result{}, fail(400, "note or tweet ID is required")
		}
		value, e := s.createEntity(ctx, user, kind, target, v, true)
		return Result{Value: value}, e
	case "BirdwatchDeleteBatSignal":
		e := s.deleteRelation(ctx, user, "note_signal", id)
		_, e2 := s.DB.ExecContext(ctx, `UPDATE fl_entities SET deleted_at=now() WHERE kind='note_signal' AND id::text=$1 AND owner_id=$2::bigint`, id, user)
		if e == nil {
			e = e2
		}
		return Result{Value: map[string]any{"success": e == nil}, Scalar: "Done"}, e
	case "BirdwatchAdmitUser", "BirdwatchAliasSelect", "BirdwatchEditNotificationSettings", "BirdwatchEditUserSettings", "BirdwatchProfileAcknowledgeEarnOut", "BirdwatchRemoveUser":
		v["admitted"] = a != "BirdwatchRemoveUser"
		return s.preference(ctx, Spec{Kind: "mutation", Entity: "birdwatch"}, user, v)
	case "BirdwatchFetchOneNote", "BirdwatchFetchNoteTranslation":
		value, e := s.getEntity(ctx, user, "note", id, true)
		if e == nil {
			s.noteCounts(ctx, id, value)
			if a == "BirdwatchFetchNoteTranslation" {
				value["translation"] = Str(value, "summary")
				value["translated"] = false
			}
		}
		return Result{Value: value}, e
	case "BirdwatchFetchAliasSelfSelectOptions":
		return Result{Value: map[string]any{"options": []string{"Helpful observer", "Careful reader", "Community contributor"}}}, nil
	case "BirdwatchFetchAuthenticatedUserProfile", "BirdwatchFetchBirdwatchProfile", "BirdwatchFetchAliasSelfSelectStatus", "BirdwatchFetchSignUpEligiblity":
		res, e := s.preference(ctx, Spec{Kind: "query", Entity: "birdwatch"}, user, v)
		if e == nil {
			res.Value["eligible"] = true
			res.Value["user_id"] = user
		}
		return res, e
	case "BirdwatchFetchCanTweetBeMediaNote":
		if tweet == "" {
			return Result{}, fail(400, "tweet_id required")
		}
		var media bool
		e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM tweet_media WHERE tweet_id::text=$1)`, tweet).Scan(&media)
		return Result{Value: map[string]any{"can_be_media_note": media}}, e
	}
	if strings.Contains(a, "BatSignal") {
		items, e := s.listEntities(ctx, user, "note_signal", tweet, "", true)
		return Result{Value: map[string]any{"items": items, "items_results": items}}, e
	}
	owner := user
	public := true
	if strings.Contains(a, "Contributor") {
		id, e := s.targetUser(ctx, user, v)
		if e != nil {
			return Result{}, e
		}
		owner = id
		public = false
	}
	items, e := s.listEntities(ctx, owner, "note", tweet, "", public)
	if e != nil {
		return Result{}, e
	}
	for _, item := range items {
		s.noteCounts(ctx, Str(item, "id"), item)
	}
	return Result{Value: map[string]any{"items": items, "items_results": items, "notes": items}}, nil
}
func (s *Store) noteCounts(ctx context.Context, id string, v map[string]any) {
	var helpful, unhelpful int
	_ = s.DB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE data->>'helpful'='true' OR data->>'helpfulnessLevel'='HELPFUL' OR data->>'rating'='helpful'),count(*) FILTER(WHERE data->>'helpful'='false' OR data->>'helpfulnessLevel'='NOT_HELPFUL' OR data->>'rating'='not_helpful') FROM fl_relations WHERE kind='note_rating' AND target_id=$1`, id).Scan(&helpful, &unhelpful)
	v["helpful_count"] = helpful
	v["not_helpful_count"] = unhelpful
	v["status"] = "NEEDS_MORE_RATINGS"
	if helpful >= 3 && helpful > unhelpful*2 {
		v["status"] = "HELPFUL"
	}
	if unhelpful >= 3 && unhelpful > helpful*2 {
		v["status"] = "NOT_HELPFUL"
	}
}
func jobInput(v map[string]any) map[string]any {
	out := vars(v)
	for _, key := range []string{"post_tweet_request", "draft_tweet", "scheduled_tweet", "tweet_info", "tweetInfo"} {
		for k, x := range object(v[key]) {
			out[k] = x
		}
	}
	if Str(out, "tweet_text") == "" {
		out["tweet_text"] = Str(out, "status", "text", "post_text")
	}
	return out
}
func (s *Store) jobs(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	kind := spec.Entity
	id := Str(v, "draftTweetId", "draft_tweet_id", "scheduledTweetId", "scheduled_tweet_id", "tweet_id", "id")
	data := jobInput(v)
	if strings.HasPrefix(a, "Fetch") {
		rows, e := s.DB.QueryContext(ctx, `SELECT id::text,data,due_at,status,tweet_id::text FROM fl_jobs WHERE owner_id=$1::bigint AND kind=$2 AND status IN ('draft','scheduled','failed') ORDER BY id DESC LIMIT $3`, user, kind, limit(v))
		if e != nil {
			return Result{}, e
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var x string
			var raw []byte
			var due sql.NullTime
			var post sql.NullString
			var status string
			if e = rows.Scan(&x, &raw, &due, &status, &post); e != nil {
				return Result{}, e
			}
			m := map[string]any{}
			_ = json.Unmarshal(raw, &m)
			m["id"] = x
			m["rest_id"] = x
			m["scheduled_at"] = due.Time
			m["execute_at"] = due.Time.Unix()
			m["status"] = status
			m["tweet_id"] = post.String
			items = append(items, m)
		}
		return Result{Value: map[string]any{"items": items, "drafts": items, "scheduled_tweets": items, "items_results": items}}, rows.Err()
	}
	if strings.HasPrefix(a, "Delete") {
		ids := []string{id}
		if a == "DeleteDraftTweets" {
			ids = stringsArray(v, "draftTweetIds", "draft_tweet_ids", "tweet_ids", "ids")
		}
		if len(ids) == 0 || ids[0] == "" {
			return Result{}, fail(400, "draft or scheduled ID required")
		}
		for _, x := range ids {
			res, e := s.DB.ExecContext(ctx, `UPDATE fl_jobs SET status='cancelled',updated_at=now() WHERE id::text=$1 AND owner_id=$2::bigint AND kind=$3 AND status IN ('draft','scheduled','failed')`, x, user, kind)
			if e != nil {
				return Result{}, e
			}
			n, _ := res.RowsAffected()
			if n == 0 {
				return Result{}, fail(404, "job not found or already published")
			}
		}
		return Result{Value: map[string]any{"success": true}, Scalar: "Done"}, nil
	}
	text := Str(data, "tweet_text")
	if strings.TrimSpace(text) == "" || len([]rune(text)) > 280 {
		return Result{}, fail(400, "draft text must contain 1-280 characters")
	}
	var due any
	status := "draft"
	if kind == "scheduled" {
		value, e := require(v, "execute_at", "scheduled_at", "scheduledAt", "publish_time")
		if e != nil {
			return Result{}, e
		}
		t, e := parseTime(value)
		if e != nil || !t.After(time.Now()) || t.After(time.Now().AddDate(1, 0, 0)) {
			return Result{}, fail(400, "schedule must be in the next year")
		}
		due = t
		status = "scheduled"
	}
	if strings.HasPrefix(a, "Create") {
		var numeric int64
		if e := s.DB.QueryRowContext(ctx, `INSERT INTO fl_jobs(owner_id,kind,data,due_at,status) VALUES($1,$2,$3,$4,$5) RETURNING id`, user, kind, Encode(data), due, status).Scan(&numeric); e != nil {
			return Result{}, e
		}
		id = strconv.FormatInt(numeric, 10)
	} else {
		res, e := s.DB.ExecContext(ctx, `UPDATE fl_jobs SET data=$1,due_at=$2,status=$3,updated_at=now() WHERE id::text=$4 AND owner_id=$5::bigint AND kind=$6 AND status IN ('draft','scheduled','failed')`, Encode(data), due, status, id, user, kind)
		if e != nil {
			return Result{}, e
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return Result{}, fail(404, "job not found or already published")
		}
	}
	return Result{Value: map[string]any{"id": id, "rest_id": id, "status": status, "scheduled_at": due, "tweet_text": text}}, nil
}
func (s *Store) PublishScheduled(ctx context.Context) error {
	for n := 0; n < 20; n++ {
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		var id, owner int64
		var raw []byte
		e = tx.QueryRowContext(ctx, `SELECT j.id,j.owner_id,j.data FROM fl_jobs j JOIN users u ON u.id=j.owner_id WHERE j.kind='scheduled' AND j.status='scheduled' AND j.due_at<=now() AND NOT u.suspended ORDER BY j.due_at FOR UPDATE OF j SKIP LOCKED LIMIT 1`).Scan(&id, &owner, &raw)
		if e == sql.ErrNoRows {
			tx.Rollback()
			return nil
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		v := map[string]any{}
		if e = json.Unmarshal(raw, &v); e != nil {
			tx.Rollback()
			return e
		}
		var tweet int64
		text := Str(v, "tweet_text")
		if strings.TrimSpace(text) == "" || len([]rune(text)) > 280 {
			_, e = tx.ExecContext(ctx, `UPDATE fl_jobs SET status='failed',data=data||'{"error":"invalid text"}'::jsonb WHERE id=$1::bigint`, id)
		} else {
			e = tx.QueryRowContext(ctx, `INSERT INTO tweets(author_id,text) VALUES($1,$2) RETURNING id`, owner, text).Scan(&tweet)
			if e == nil {
				for i, media := range stringsArray(v, "media_ids") {
					var ready bool
					if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM media WHERE id::text=$1 AND owner_id=$2::bigint AND status='ready')`, media, owner).Scan(&ready); err != nil || !ready {
						e = fail(400, "scheduled media is unavailable")
						break
					}
					_, e = tx.ExecContext(ctx, `INSERT INTO tweet_media(tweet_id,media_id,position) VALUES($1,$2,$3)`, tweet, media, i)
					if e != nil {
						break
					}
				}
			}
			if e == nil {
				_, e = tx.ExecContext(ctx, `UPDATE fl_jobs SET status='published',tweet_id=$1,updated_at=now() WHERE id=$2::bigint`, tweet, id)
			}
		}
		if e != nil {
			tx.Rollback()
			_, _ = s.DB.ExecContext(ctx, `UPDATE fl_jobs SET status='failed',data=data||$1 WHERE id=$2::bigint AND status='scheduled'`, Encode(map[string]any{"error": ErrorMessage(e)}), id)
			continue
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}
