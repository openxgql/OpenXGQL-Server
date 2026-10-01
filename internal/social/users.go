package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"regexp"
	"strconv"
)

func (s *Store) targetUser(ctx context.Context, user string, v map[string]any) (string, error) {
	id := Str(v, "userId", "user_id", "targetUserId", "target_user_id", "restId", "rest_id", "id")
	if id != "" {
		return id, s.exists(ctx, "users", id)
	}
	name := Str(v, "screenName", "screen_name", "username")
	if name == "" {
		return user, nil
	}
	e := s.DB.QueryRowContext(ctx, `SELECT id::text FROM users WHERE lower(screen_name)=lower($1)`, name).Scan(&id)
	if e == sql.ErrNoRows {
		return "", fail(404, "user not found")
	}
	return id, e
}
func (s *Store) follow(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	action := spec.Action
	if action == "FollowerRequests" {
		rows, e := s.DB.QueryContext(ctx, `SELECT owner_id::text FROM fl_relations WHERE kind='follow_request' AND target_id=$1 ORDER BY created_at DESC LIMIT $2`, user, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{UserIDs: ids, Timeline: true, UserID: user}, e
	}
	target, e := require(v, "targetUserId", "target_user_id", "userId", "user_id", "sourceUserId", "source_user_id", "restId")
	if e != nil {
		return Result{}, e
	}
	if e = s.exists(ctx, "users", target); e != nil {
		return Result{}, e
	}
	if target == user {
		return Result{}, fail(400, "cannot follow yourself")
	}
	var blocked bool
	if e = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fl_relations WHERE kind='block' AND ((owner_id=$1::bigint AND target_id=$2::text) OR (owner_id=$2::bigint AND target_id=$1::text)))`, user, target).Scan(&blocked); e != nil {
		return Result{}, e
	}
	if blocked {
		return Result{}, fail(403, "relationship blocked")
	}
	state := map[string]any{"success": true, "following": false, "follow_request_sent": false, "__typename": "UnfollowSuccessResult"}
	switch action {
	case "FollowUser":
		var protected bool
		if e = s.DB.QueryRowContext(ctx, `SELECT protected FROM users WHERE id=$1::bigint`, target).Scan(&protected); e != nil {
			return Result{}, e
		}
		if protected {
			e = s.putRelation(ctx, user, "follow_request", target, v)
			state["follow_request_sent"] = true
			state["__typename"] = "FollowRequestSuccessResult"
		} else {
			_, e = s.DB.ExecContext(ctx, `INSERT INTO follows(follower_id,target_id) VALUES($1,$2) ON CONFLICT(follower_id,target_id) DO NOTHING`, user, target)
			state["following"] = true
			state["__typename"] = "FollowSuccessResult"
		}
	case "UnfollowUser", "CancelFollowRequest":
		_, e = s.DB.ExecContext(ctx, `DELETE FROM follows WHERE follower_id=$1::bigint AND target_id=$2`, user, target)
		if e == nil {
			e = s.deleteRelation(ctx, user, "follow_request", target)
		}
	case "RemoveFollower":
		_, e = s.DB.ExecContext(ctx, `DELETE FROM follows WHERE follower_id=$1::bigint AND target_id=$2`, target, user)
	case "AcceptFollowRequest", "DenyFollowRequest":
		tx, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			return Result{}, err
		}
		defer tx.Rollback()
		res, err := tx.ExecContext(ctx, `DELETE FROM fl_relations WHERE owner_id=$1::bigint AND target_id=$2 AND kind='follow_request'`, target, user)
		if err != nil {
			return Result{}, err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return Result{}, fail(404, "follow request not found")
		}
		if action == "AcceptFollowRequest" {
			if _, err = tx.ExecContext(ctx, `INSERT INTO follows(follower_id,target_id) VALUES($1,$2) ON CONFLICT(follower_id,target_id) DO NOTHING`, target, user); err != nil {
				return Result{}, err
			}
		}
		e = tx.Commit()
	}
	return Result{Value: state, Scalar: "Done"}, e
}
func (s *Store) users(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	id, e := s.targetUser(ctx, user, v)
	if e != nil && spec.Handler != "reaction_users" && spec.Handler != "lookup" {
		return Result{}, e
	}
	ids := []string{}
	switch spec.Handler {
	case "follow_list":
		query := `SELECT follower_id::text FROM follows WHERE target_id=$1`
		if a == "Following" {
			query = `SELECT target_id::text FROM follows WHERE follower_id=$1::bigint`
		}
		if a == "BlueVerifiedFollowers" {
			query = `SELECT f.follower_id::text FROM follows f JOIN users u ON u.id=f.follower_id WHERE f.target_id=$1 AND u.verified`
		}
		if a == "FollowersYouKnow" {
			query = `SELECT f.follower_id::text FROM follows f WHERE f.target_id=$1 AND EXISTS(SELECT 1 FROM follows mine WHERE mine.follower_id=$2::bigint AND mine.target_id=f.follower_id)`
			rows, err := s.DB.QueryContext(ctx, query+` ORDER BY f.id DESC LIMIT $3`, id, user, limit(v))
			ids, e = scanIDs(rows, err)
		} else {
			order := "id"
			if a == "BlueVerifiedFollowers" {
				order = "f.id"
			}
			rows, err := s.DB.QueryContext(ctx, query+` ORDER BY `+order+` DESC LIMIT $2`, id, limit(v))
			ids, e = scanIDs(rows, err)
		}
	case "relationship_list":
		kind := "block"
		if a == "MutedAccounts" {
			kind = "mute"
		}
		ids, e = s.relationIDs(ctx, user, kind)
		id = user
	case "reaction_users":
		tweet, e := require(v, "tweetId", "tweet_id", "restId", "rest_id")
		if e != nil {
			return Result{}, e
		}
		if e = s.exists(ctx, "tweets", tweet); e != nil {
			return Result{}, e
		}
		table := "reactions"
		if a == "Retweeters" {
			table = "retweets"
		}
		rows, err := s.DB.QueryContext(ctx, `SELECT user_id::text FROM `+table+` WHERE tweet_id=$1 ORDER BY user_id DESC LIMIT $2`, tweet, limit(v))
		ids, e = scanIDs(rows, err)
	case "lookup":
		switch a {
		case "UserByID", "UserByName", "ImmersiveProfile":
			id, e = s.targetUser(ctx, user, v)
			if e != nil {
				return Result{}, e
			}
			ids = []string{id}
		case "TweetByID":
			tweet, e := require(v, "tweetId", "tweet_id", "restId", "rest_id", "focalTweetId")
			if e != nil {
				return Result{}, e
			}
			return Result{TweetIDs: []string{tweet}}, s.exists(ctx, "tweets", tweet)
		case "TweetResultsByRestIds":
			ids = stringsArray(v, "tweetIds", "tweet_ids", "rest_ids")
			return Result{TweetIDs: ids}, nil
		case "UsersByRestIds", "UsersVerifiedAvatars":
			ids = stringsArray(v, "userIds", "user_ids", "restIds", "rest_ids", "ids")
			if len(ids) == 0 {
				return Result{}, fail(400, "user IDs are required")
			}
			valid := []string{}
			for _, id := range ids {
				if s.exists(ctx, "users", id) == nil {
					valid = append(valid, id)
				}
			}
			ids = valid
		case "UsersByScreenNames":
			for _, name := range stringsArray(v, "screenNames", "screen_names") {
				var x string
				if s.DB.QueryRowContext(ctx, `SELECT id::text FROM users WHERE lower(screen_name)=lower($1)`, name).Scan(&x) == nil {
					ids = append(ids, x)
				}
			}
		case "UsersForTweet":
			tweet, e := require(v, "tweetId", "tweet_id", "restId", "rest_id")
			if e != nil {
				return Result{}, e
			}
			rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT author_id::text FROM tweets WHERE id::text=$1 OR reply_to_id::text=$1 LIMIT $2`, tweet, limit(v))
			ids, e = scanIDs(rows, err)
		}
	}
	return Result{UserIDs: ids, UserID: id, Timeline: spec.Handler != "lookup", Value: map[string]any{"id": "users-" + id}}, e
}
func stringsArray(v map[string]any, keys ...string) []string {
	for _, key := range keys {
		if a, ok := v[key].([]any); ok {
			out := []string{}
			for _, x := range a {
				switch t := x.(type) {
				case string:
					out = append(out, t)
				case float64:
					out = append(out, strconv.FormatInt(int64(t), 10))
				case map[string]any:
					if id := Str(t, "id", "rest_id"); id != "" {
						out = append(out, id)
					}
				}
			}
			return out
		}
	}
	return []string{}
}

var username = regexp.MustCompile(`^[A-Za-z0-9_]{1,15}$`)

func (s *Store) nameAvailability(ctx context.Context, v map[string]any) (Result, error) {
	name, e := require(v, "screen_name", "screenName", "username")
	if e != nil {
		return Result{}, e
	}
	valid := username.MatchString(name)
	var exists bool
	if valid {
		if e = s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(screen_name)=lower($1))`, name).Scan(&exists); e != nil {
			return Result{}, e
		}
	}
	suggestions := []string{}
	if exists {
		prefix := name
		if len(prefix) > 11 {
			prefix = prefix[:11]
		}
		for n := 1; len(suggestions) < 3 && n < 1000; n++ {
			candidate := prefix + strconv.Itoa(n)
			var used bool
			if s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(screen_name)=lower($1))`, candidate).Scan(&used) == nil && !used {
				suggestions = append(suggestions, candidate)
			}
		}
	}
	return Result{Value: map[string]any{"available": valid && !exists, "valid": valid, "suggestions": suggestions, "reason": "", "screen_name": name}}, nil
}
func (s *Store) profileState(ctx context.Context, user string) (map[string]any, error) {
	var name, handle string
	var verified, premium, protected bool
	if e := s.DB.QueryRowContext(ctx, `SELECT name,screen_name,verified,premium,protected FROM users WHERE id=$1::bigint`, user).Scan(&name, &handle, &verified, &premium, &protected); e != nil {
		return nil, e
	}
	var pref []byte
	_ = s.DB.QueryRowContext(ctx, `SELECT data FROM fl_preferences WHERE owner_id=$1::bigint AND key='preferences'`, user).Scan(&pref)
	v := map[string]any{}
	_ = json.Unmarshal(pref, &v)
	v["id"] = user
	v["screen_name"] = handle
	v["name"] = name
	v["verified"] = verified
	v["premium"] = premium
	v["protected"] = protected
	return v, nil
}
