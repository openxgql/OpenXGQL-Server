package social

import (
	"context"
	"database/sql"
	"encoding/json"
)

func (s *Store) UserState(ctx context.Context, id, viewer string) (map[string]any, error) {
	if viewer == "" {
		viewer = "0"
	}
	var protected, suspended, blocking, blockedBy, muting, requested bool
	e := s.DB.QueryRowContext(ctx, `SELECT protected,suspended,
 EXISTS(SELECT 1 FROM fl_relations WHERE owner_id=$2::bigint AND kind='block' AND target_id=$1::text),
 EXISTS(SELECT 1 FROM fl_relations WHERE owner_id=$1::bigint AND kind='block' AND target_id=$2::text),
 EXISTS(SELECT 1 FROM fl_relations WHERE owner_id=$2::bigint AND kind='mute' AND target_id=$1::text),
 EXISTS(SELECT 1 FROM fl_relations WHERE owner_id=$2::bigint AND kind='follow_request' AND target_id=$1::text)
 FROM users WHERE id=$1::bigint`, id, viewer).Scan(&protected, &suspended, &blocking, &blockedBy, &muting, &requested)
	if e == sql.ErrNoRows {
		return nil, fail(404, "user not found")
	}
	if e != nil {
		return nil, e
	}
	if suspended {
		return nil, fail(403, "account is suspended")
	}
	pinned, e := s.relationIDs(ctx, id, "pin_tweet")
	return map[string]any{"protected": protected, "blocking": blocking, "blocked_by": blockedBy, "muting": muting, "follow_request_sent": requested, "pinned": pinned}, e
}
func (s *Store) PostMeta(ctx context.Context, id, viewer string) (map[string]any, error) {
	if viewer == "" {
		viewer = "0"
	}
	var bookmarked, highlighted, pinned bool
	var bookmarks, quotes int
	e := s.DB.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM fl_relations WHERE kind='bookmark' AND owner_id=$2::bigint AND target_id=$1),
 EXISTS(SELECT 1 FROM fl_relations WHERE kind='highlight' AND owner_id=$2::bigint AND target_id=$1),
 EXISTS(SELECT 1 FROM fl_relations WHERE kind='pin_tweet' AND target_id=$1),
 (SELECT count(*) FROM fl_relations WHERE kind='bookmark' AND target_id=$1),
 (SELECT count(*) FROM fl_relations WHERE kind='quote' AND data->>'quoted_tweet_id'=$1)`, id, viewer).Scan(&bookmarked, &highlighted, &pinned, &bookmarks, &quotes)
	if e != nil {
		return nil, e
	}
	value := map[string]any{"bookmarked": bookmarked, "bookmark_count": bookmarks, "quote_count": quotes, "highlighted": highlighted, "pinned": pinned}
	var raw []byte
	e = s.DB.QueryRowContext(ctx, `SELECT data FROM fl_relations WHERE kind='post_options' AND target_id=$1 LIMIT 1`, id).Scan(&raw)
	if e != nil && e != sql.ErrNoRows {
		return nil, e
	}
	if len(raw) > 0 {
		var options map[string]any
		_ = json.Unmarshal(raw, &options)
		for k, v := range options {
			value[k] = v
		}
	}
	return value, nil
}
func (s *Store) ValidatePost(ctx context.Context, user string, v map[string]any) error {
	reply := Str(object(v["reply"]), "in_reply_to_tweet_id")
	quote := Str(v, "quoted_tweet_id", "quoteTweetId")
	for _, id := range []string{reply, quote} {
		if id != "" {
			ids, e := s.VisiblePosts(ctx, user, []string{id})
			if e != nil {
				return e
			}
			if len(ids) == 0 {
				return fail(404, "post unavailable")
			}
		}
	}
	if reply != "" {
		var raw []byte
		var owner string
		e := s.DB.QueryRowContext(ctx, `SELECT t.author_id::text,p.data FROM tweets t LEFT JOIN fl_preferences p ON p.owner_id=t.author_id AND p.key='conversation_controls/'||t.id::text WHERE t.id::text=$1`, reply).Scan(&owner, &raw)
		if e != nil {
			return e
		}
		var controls map[string]any
		_ = json.Unmarshal(raw, &controls)
		mode := Str(controls, "conversation_control", "mode")
		if owner != user && (mode == "ByInvitation" || mode == "OnlyMentionedUsers") {
			return fail(403, "conversation replies are restricted")
		}
		if owner != user && (mode == "Community" || mode == "Following") {
			var following bool
			if e = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM follows WHERE follower_id=$1::bigint AND target_id=$2)`, owner, user).Scan(&following); e != nil {
				return e
			}
			if !following {
				return fail(403, "conversation replies require a follow")
			}
		}
	}
	if community := Str(v, "community_id", "communityId"); community != "" {
		var member bool
		if e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fl_relations WHERE owner_id=$1::bigint AND kind='community_member' AND target_id=$2)`, user, community).Scan(&member); e != nil {
			return e
		}
		if !member {
			return fail(403, "community membership required")
		}
	}
	return nil
}
func (s *Store) CommitPost(ctx context.Context, user, id string, v map[string]any) error {
	var owner string
	if e := s.DB.QueryRowContext(ctx, `SELECT author_id::text FROM tweets WHERE id::text=$1`, id).Scan(&owner); e != nil {
		return e
	}
	if owner != user {
		return fail(403, "post belongs to another user")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	put := func(kind string, data any) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO fl_relations(owner_id,kind,target_id,data) VALUES($1,$2,$3,$4) ON CONFLICT(owner_id,kind,target_id) DO UPDATE SET data=EXCLUDED.data`, user, kind, id, Encode(data))
		return err
	}
	if community := Str(v, "community_id", "communityId"); community != "" {
		if e = put("community_post", map[string]any{"community_id": community}); e != nil {
			return e
		}
	}
	quote := Str(v, "quoted_tweet_id", "quoteTweetId")
	if quote != "" {
		if e = put("quote", map[string]any{"quoted_tweet_id": quote}); e != nil {
			return e
		}
	}
	options := map[string]any{}
	if quote != "" {
		options["quoted_tweet_id"] = quote
	}
	if community := Str(v, "community_id", "communityId"); community != "" {
		options["community_id"] = community
	}
	if len(options) > 0 {
		if e = put("post_options", options); e != nil {
			return e
		}
	}
	return tx.Commit()
}
