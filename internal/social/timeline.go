package social

import (
	"context"
	"strconv"
	"strings"
	"time"
)

const visibility = `NOT t.hidden AND NOT t.is_ad AND NOT u.suspended AND (NOT u.protected OR t.author_id=$1::bigint OR EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1::bigint AND f.target_id=t.author_id)) AND NOT EXISTS(SELECT 1 FROM fl_relations b WHERE (b.owner_id=$1::bigint AND b.kind IN ('block','mute') AND b.target_id=t.author_id::text) OR (b.owner_id=t.author_id AND b.kind='block' AND b.target_id=$1::text))`

func (s *Store) VisiblePosts(ctx context.Context, user string, ids []string) ([]string, error) {
	if len(ids) > 200 {
		return nil, fail(400, "too many posts")
	}
	if user == "" {
		user = "0"
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text FROM tweets t JOIN users u ON u.id=t.author_id WHERE t.id::text=ANY($2::text[]) AND NOT t.hidden AND NOT u.suspended AND (NOT u.protected OR t.author_id=$1::bigint OR EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1::bigint AND f.target_id=t.author_id)) AND NOT EXISTS(SELECT 1 FROM fl_relations b WHERE b.kind='block' AND ((b.owner_id=$1::bigint AND b.target_id=t.author_id::text) OR (b.owner_id=t.author_id AND b.target_id=$1::text))) ORDER BY array_position($2::text[],t.id::text)`, user, ids)
	return scanIDs(rows, e)
}
func (s *Store) timeline(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	if a == "GetUsernameAvailabilityAndSuggestions" || a == "screen_nameAvailabilityMutation" {
		return s.nameAvailability(ctx, v)
	}
	if spec.Handler == "discovery" {
		if a == "ConnectTabTimeline" || a == "SidebarUserRecommendations" || a == "TrendRelevantUsers" {
			ids, e := s.recommendedUsers(ctx, user, limit(v))
			return Result{UserIDs: ids, Timeline: true}, e
		}
		if a == "ExploreSidebar" {
			trends, e := s.trends(ctx, user)
			if e != nil {
				return Result{}, e
			}
			trends = trends[:min(len(trends), limit(v))]
			ids, e := s.recommendedUsers(ctx, user, min(3, limit(v)))
			return Result{UserIDs: ids, Timeline: true, Value: map[string]any{"trends": trends, "id": "explore-sidebar"}}, e
		}
		if a == "TopicByRestId" {
			value, e := s.getEntity(ctx, user, "topic", Str(v, "topicId", "topic_id", "restId", "rest_id"), true)
			return Result{Value: value}, e
		}
		if a == "TopicToFollowSidebar" {
			items, e := s.listEntities(ctx, user, "topic", "", "", true)
			return Result{Value: map[string]any{"items": items, "items_results": items}}, e
		}
		if a == "TrendHistory" {
			ids, e := s.relationIDs(ctx, user, "saved_trend")
			return Result{Value: map[string]any{"items": ids}}, e
		}
		if a == "AiTrendById" || a == "AiTrendByRestId" || a == "StoryTopicStories" {
			return s.catalog(ctx, Spec{Action: a, Entity: "trend", Kind: spec.Kind}, user, v)
		}
		trends, e := s.trends(ctx, user)
		if e != nil {
			return Result{}, e
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text FROM tweets t JOIN users u ON u.id=t.author_id WHERE `+visibility+` ORDER BY t.id DESC LIMIT $2`, user, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{TweetIDs: ids, Timeline: true, Value: map[string]any{"trends": trends, "items": trends, "id": "explore"}}, e
	}
	if a == "Typeahead" || a == "TagTypeahead" || a == "FinanceSearchTags" {
		q := strings.TrimSpace(Str(v, "query", "rawQuery", "raw_query", "q"))
		if a != "Typeahead" {
			trends, e := s.trends(ctx, user)
			items := []map[string]any{}
			for _, t := range trends {
				if strings.Contains(strings.ToLower(Str(t, "name")), strings.ToLower(q)) {
					items = append(items, t)
				}
			}
			return Result{Value: map[string]any{"items": items, "query_results": items}}, e
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT id::text FROM users WHERE NOT suspended AND (screen_name ILIKE $1||'%' OR name ILIKE '%'||$1||'%') ORDER BY id DESC LIMIT 20`, q)
		ids, e := scanIDs(rows, e)
		return Result{UserIDs: ids, Value: map[string]any{"query_results": []any{}, "topics": []any{}}}, e
	}
	uid := user
	filter := ""
	args := []any{user}
	arg := func(value any) string { args = append(args, value); return "$" + strconv.Itoa(len(args)) }
	switch a {
	case "LikesTimeline":
		id, e := s.targetUser(ctx, user, v)
		if e != nil {
			return Result{}, e
		}
		uid = id
		filter = ` AND EXISTS(SELECT 1 FROM reactions r WHERE r.tweet_id=t.id AND r.user_id=` + arg(id) + `)`
	case "UserOriginalsTimeline", "UserRepliesTimeline", "UserTweets", "UserTweetsAndReplies", "UserVideoTimeline", "UserPhotoTimeline", "UserMedia", "UserHighlightsTweets", "UserRepostsTimeline", "UserArticlesTweets", "UserPromotableTweets", "UserPromotedTweets", "UserBusinessProfileTeamTimeline":
		id, e := s.targetUser(ctx, user, v)
		if e != nil {
			return Result{}, e
		}
		uid = id
		filter = ` AND t.author_id=` + arg(id)
		if a == "UserOriginalsTimeline" {
			filter += ` AND t.reply_to_id IS NULL`
		}
		if a == "UserRepliesTimeline" {
			filter += ` AND t.reply_to_id IS NOT NULL`
		}
		if a == "UserRepostsTimeline" {
			filter = ` AND EXISTS(SELECT 1 FROM retweets r WHERE r.tweet_id=t.id AND r.user_id=` + arg(id) + `)`
		}
		if a == "UserHighlightsTweets" {
			filter = ` AND EXISTS(SELECT 1 FROM fl_relations r WHERE r.owner_id=` + arg(id) + ` AND r.kind='highlight' AND r.target_id=t.id::text)`
		}
		if a == "UserPhotoTimeline" || a == "UserVideoTimeline" || a == "UserMedia" {
			mime := "%"
			if a == "UserPhotoTimeline" {
				mime = "image/%"
			}
			if a == "UserVideoTimeline" {
				mime = "video/%"
			}
			filter += ` AND EXISTS(SELECT 1 FROM tweet_media tm JOIN media m ON m.id=tm.media_id WHERE tm.tweet_id=t.id AND m.mime LIKE ` + arg(mime) + `)`
		}
		if a == "UserArticlesTweets" {
			filter += ` AND EXISTS(SELECT 1 FROM fl_entities e WHERE e.kind='article' AND e.data->>'tweet_id'=t.id::text AND e.deleted_at IS NULL AND e.public)`
		}
	case "ModeratedTimeline":
		focal, e := require(v, "focalTweetId", "tweetId", "tweet_id", "restId", "rest_id")
		if e != nil {
			return Result{}, e
		}
		filter = ` AND t.reply_to_id::text=` + arg(focal) + ` AND EXISTS(SELECT 1 FROM fl_relations r WHERE r.kind='hide_reply' AND r.target_id=t.id::text)`
	case "TweetEditHistory":
		focal, e := require(v, "tweetId", "tweet_id", "restId", "rest_id")
		if e != nil {
			return Result{}, e
		}
		items, e := s.listEntities(ctx, user, "tweet_edit", focal, "", true)
		return Result{Value: map[string]any{"items": items, "edit_history": items}, TweetIDs: []string{focal}}, e
	case "SimilarPosts":
		id, e := require(v, "tweetId", "tweet_id", "restId", "rest_id")
		if e != nil {
			return Result{}, e
		}
		var text string
		if e = s.DB.QueryRowContext(ctx, `SELECT text FROM tweets WHERE id::text=$1`, id).Scan(&text); e != nil {
			return Result{}, e
		}
		word := ""
		for _, w := range strings.Fields(text) {
			if len([]rune(w)) > 3 {
				word = w
				break
			}
		}
		if word == "" {
			return Result{TweetIDs: []string{}, Timeline: true}, nil
		}
		filter = ` AND t.id::text<>` + arg(id) + ` AND t.text ILIKE '%'||` + arg(word) + `||'%'`
	}
	if spec.Handler == "search" || strings.Contains(a, "SearchTimeline") {
		q := strings.TrimSpace(Str(v, "rawQuery", "raw_query", "query", "q"))
		if q == "" {
			return Result{}, fail(400, "search query is required")
		}
		if strings.EqualFold(Str(v, "product", "searchProduct"), "People") {
			rows, e := s.DB.QueryContext(ctx, `SELECT id::text FROM users WHERE NOT suspended AND (screen_name ILIKE '%'||$1||'%' OR name ILIKE '%'||$1||'%') ORDER BY id DESC LIMIT $2`, q, limit(v))
			ids, e := scanIDs(rows, e)
			return Result{UserIDs: ids, Timeline: true}, e
		}
		for _, w := range strings.Fields(q) {
			if strings.HasPrefix(strings.ToLower(w), "from:") {
				handle := strings.TrimPrefix(w, "from:")
				handle = strings.TrimPrefix(handle, "@")
				filter += ` AND u.screen_name ILIKE ` + arg(handle)
			} else if w != "filter:media" && w != "filter:images" && w != "filter:videos" {
				filter += ` AND t.text ILIKE '%'||` + arg(w) + `||'%'`
			}
		}
		product := strings.ToLower(Str(v, "product"))
		if product == "photos" || strings.Contains(q, "filter:images") {
			filter += ` AND EXISTS(SELECT 1 FROM tweet_media tm JOIN media m ON m.id=tm.media_id WHERE tm.tweet_id=t.id AND m.mime LIKE 'image/%')`
		}
		if product == "videos" || strings.Contains(q, "filter:videos") {
			filter += ` AND EXISTS(SELECT 1 FROM tweet_media tm JOIN media m ON m.id=tm.media_id WHERE tm.tweet_id=t.id AND m.mime LIKE 'video/%')`
		}
	}
	if cursor := Str(v, "cursor"); cursor != "" {
		if !validID(cursor) {
			return Result{}, fail(400, "invalid cursor")
		}
		filter += ` AND t.id<` + arg(cursor)
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text FROM tweets t JOIN users u ON u.id=t.author_id WHERE `+visibility+filter+` ORDER BY t.id DESC LIMIT `+arg(limit(v)), args...)
	ids, e := scanIDs(rows, e)
	cursor := ""
	if len(ids) == limit(v) {
		cursor = ids[len(ids)-1]
	}
	return Result{TweetIDs: ids, Timeline: true, UserID: uid, Cursor: cursor, Value: map[string]any{"id": a + "-" + uid}}, e
}
func (s *Store) recommendedUsers(ctx context.Context, user string, count int) ([]string, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT u.id::text FROM users u WHERE u.id<>$1 AND NOT u.suspended AND NOT u.protected AND NOT EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1::bigint AND f.target_id=u.id) AND NOT EXISTS(SELECT 1 FROM fl_relations b WHERE (b.owner_id=$1::bigint AND b.kind IN ('block','mute') AND b.target_id=u.id::text) OR (b.owner_id=u.id AND b.kind='block' AND b.target_id=$1::text)) ORDER BY (SELECT count(*) FROM follows f WHERE f.target_id=u.id) DESC,u.id DESC LIMIT $2`, user, count)
	return scanIDs(rows, e)
}
func (s *Store) trends(ctx context.Context, user string) ([]map[string]any, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT lower(m.tag[1]),count(*) FROM tweets t JOIN users u ON u.id=t.author_id CROSS JOIN LATERAL regexp_matches(t.text,'(#[:a-zA-Z0-9_А-Яа-я]+)','g') m(tag) WHERE NOT t.hidden AND NOT t.is_ad AND NOT u.suspended AND NOT u.protected GROUP BY 1 ORDER BY count(*) DESC,1 LIMIT 50`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var tag string
		var n int
		if e = rows.Scan(&tag, &n); e != nil {
			return nil, e
		}
		items = append(items, map[string]any{"name": tag, "query": tag, "tweet_count": n, "__typename": "TimelineTrend"})
	}
	return items, rows.Err()
}
func (s *Store) notifications(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	var count int
	e := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM fl_notifications WHERE user_id=$1::bigint AND read_at IS NULL`, user).Scan(&count)
	if e != nil {
		return Result{}, e
	}
	if spec.Action == "ViewerBadgeCounts" {
		return Result{Value: map[string]any{"notifications": count, "notification_count": count, "dm_unread_count": 0}}, nil
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT n.id::text,n.actor_id::text,n.kind,COALESCE(n.tweet_id::text,''),n.created_at FROM fl_notifications n WHERE n.user_id=$1::bigint AND NOT EXISTS(SELECT 1 FROM fl_relations m WHERE m.owner_id=$1::bigint AND m.kind IN ('mute','block') AND m.target_id=n.actor_id::text) ORDER BY n.id DESC LIMIT $2`, user, limit(v))
	if e != nil {
		return Result{}, e
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, actor, kind, tweet string
		var created time.Time
		if e = rows.Scan(&id, &actor, &kind, &tweet, &created); e != nil {
			return Result{}, e
		}
		item := map[string]any{"id": id, "kind": kind, "actor_id": actor, "user_results": map[string]any{"result": map[string]any{"__user_id": actor}}, "created_at": created, "tweet_id": tweet}
		if tweet != "" {
			item["tweet_results"] = map[string]any{"result": map[string]any{"__tweet_id": tweet}}
		}
		items = append(items, item)
	}
	if e = rows.Err(); e != nil {
		return Result{}, e
	}
	_, e = s.DB.ExecContext(ctx, `UPDATE fl_notifications SET read_at=now() WHERE user_id=$1::bigint AND read_at IS NULL`, user)
	return Result{UserID: user, Value: map[string]any{"items": items, "unread_count": count, "id": "notifications-" + user}}, e
}
func (s *Store) pinned(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	if spec.Action == "UpdatePinnedTimelines" {
		ids := stringsArray(v, "pinnedTimelineIds", "timeline_ids", "pinned_timeline_ids", "pinned_timelines")
		if len(ids) > 10 {
			return Result{}, fail(400, "at most 10 pinned timelines")
		}
		tx, e := s.DB.BeginTx(ctx, nil)
		if e != nil {
			return Result{}, e
		}
		defer tx.Rollback()
		if _, e = tx.ExecContext(ctx, `DELETE FROM fl_relations WHERE owner_id=$1::bigint AND kind='pin_timeline'`, user); e != nil {
			return Result{}, e
		}
		for i, id := range ids {
			if len(id) > 100 {
				return Result{}, fail(400, "invalid timeline ID")
			}
			if _, e = tx.ExecContext(ctx, `INSERT INTO fl_relations(owner_id,kind,target_id,data) VALUES($1,'pin_timeline',$2,$3)`, user, id, Encode(map[string]any{"position": i})); e != nil {
				return Result{}, e
			}
		}
		if e = tx.Commit(); e != nil {
			return Result{}, e
		}
	}
	ids, e := s.relationIDs(ctx, user, "pin_timeline")
	items := []map[string]any{}
	for _, id := range ids {
		items = append(items, map[string]any{"id": id, "rest_id": id, "name": id, "__typename": "PinnedTimeline", "type": "List", "timeline_id": id})
	}
	return Result{Value: map[string]any{"pinned_timelines": items, "items": items}}, e
}
