package social

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"strings"
	"time"
)

func (s *Store) catalogRecord(ctx context.Context, user, kind, id string) (map[string]any, error) {
	var internal string
	e := s.DB.QueryRowContext(ctx, `SELECT id::text FROM fl_entities WHERE kind=$1 AND deleted_at IS NULL AND (public OR owner_id=$2::bigint) AND (id::text=$3 OR data->>'rest_id'=$3 OR data->>'external_id'=$3) LIMIT 1`, kind, user, id).Scan(&internal)
	if e == sql.ErrNoRows {
		return nil, fail(404, "catalog record not found")
	}
	if e != nil {
		return nil, e
	}
	return s.getEntity(ctx, user, kind, internal, true)
}
func (s *Store) catalog(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	kind := spec.Entity
	a := spec.Action
	id := Str(v, "spaceId", "audio_space_id", "broadcastId", "broadcast_id", "eventId", "event_id", "restId", "rest_id", "id")
	q := Str(v, "query", "searchTerm", "search_term", "raw_query")
	if spec.Kind == "mutation" {
		if id == "" {
			return Result{}, fail(400, "catalog record ID required")
		}
		record, e := s.catalogRecord(ctx, user, kind, id)
		if e != nil {
			return Result{}, e
		}
		if strings.Contains(strings.ToLower(a), "viewersession") || a == "BroadcastUpdateViewerSession" {
			e = s.putRelation(ctx, user, "broadcast_view", id, map[string]any{"last_seen": time.Now()})
			if e != nil {
				return Result{}, e
			}
			record["viewer_session"] = map[string]any{"user_id": user, "broadcast_id": id, "last_seen": time.Now()}
			return Result{Value: record}, nil
		}
		if strings.Contains(strings.ToLower(a), "reminder") {
			k := "event_reminder"
			if Bool(v, "enabled") || strings.Contains(strings.ToLower(a), "create") {
				e = s.putRelation(ctx, user, k, id, v)
			} else {
				e = s.deleteRelation(ctx, user, k, id)
			}
			return Result{Value: record, Scalar: "Done"}, e
		}
		if Str(record, "owner_id") != user {
			return Result{}, fail(403, "only the record owner can change the catalog")
		}
		value, e := s.updateEntity(ctx, user, kind, Str(record, "id"), v, false)
		return Result{Value: value}, e
	}
	if a == "AuthenticatePeriscope" || a == "proxseeLoginQuery" {
		return Result{Value: map[string]any{"user_id": user, "authenticated": true, "provider": "flashlarry"}}, nil
	}
	if id != "" {
		value, e := s.catalogRecord(ctx, user, kind, id)
		if e != nil {
			return Result{}, e
		}
		if kind == "broadcast" {
			var viewers int
			_ = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM fl_relations WHERE kind='broadcast_view' AND target_id=$1 AND created_at>now()-interval '2 minutes'`, id).Scan(&viewers)
			value["live_viewer_count"] = viewers
		}
		return Result{Value: value}, nil
	}
	items, e := s.listEntities(ctx, user, kind, "", q, true)
	if e != nil {
		return Result{}, e
	}
	return Result{Value: map[string]any{"items": items, "items_results": items, "results": items, "categories": items, "topics": items}}, nil
}
func (s *Store) sports(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	team := Str(v, "teamId", "team_id", "sports_team_id", "restId", "rest_id")
	game := Str(v, "gameId", "game_id", "sports_game_id")
	target := team
	if target == "" {
		target = game
	}
	if spec.Kind == "mutation" {
		if target == "" {
			return Result{}, fail(400, "team or game ID required")
		}
		record, e := s.catalogRecord(ctx, user, "sports", target)
		if e != nil {
			return Result{}, e
		}
		kind := "sports_follow"
		action := "put"
		lower := strings.ToLower(a)
		if strings.Contains(lower, "reminder") {
			kind = "sports_reminder"
		}
		if strings.Contains(lower, "notification") {
			kind = "sports_notifications"
		}
		if strings.Contains(lower, "pick") {
			kind = "sports_pick"
			if game == "" {
				return Result{}, fail(400, "gameId required")
			}
			target = game
		}
		if strings.Contains(lower, "unfollow") || strings.Contains(lower, "delete") || strings.Contains(lower, "optout") {
			action = "delete"
		}
		if action == "put" {
			e = s.putRelation(ctx, user, kind, target, v)
		} else {
			e = s.deleteRelation(ctx, user, kind, target)
		}
		record["success"] = e == nil
		record["following"] = action == "put"
		return Result{Value: record, Scalar: "Done"}, e
	}
	if strings.Contains(a, "FollowedTeams") {
		ids, e := s.relationIDs(ctx, user, "sports_follow")
		items := []map[string]any{}
		for _, id := range ids {
			r, err := s.catalogRecord(ctx, user, "sports", id)
			if err == nil {
				items = append(items, r)
			}
		}
		return Result{Value: map[string]any{"items": items, "teams": items}}, e
	}
	if strings.Contains(a, "GameReminders") || strings.Contains(a, "NotificationSettings") || a == "SportsTeamRosterFollowState" {
		kind := "sports_reminder"
		if strings.Contains(a, "Notification") {
			kind = "sports_notifications"
		}
		if a == "SportsTeamRosterFollowState" {
			kind = "sports_follow"
		}
		ids, e := s.relationIDs(ctx, user, kind)
		return Result{Value: map[string]any{"items": ids, "following": contains(ids, target), "enabled": contains(ids, target)}}, e
	}
	if strings.Contains(a, "Tallies") {
		rows, e := s.DB.QueryContext(ctx, `SELECT data->>'teamId',count(*) FROM fl_relations WHERE kind='sports_pick' AND ($1='' OR target_id=$1) GROUP BY data->>'teamId'`, game)
		if e != nil {
			return Result{}, e
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var team sql.NullString
			var n int
			if e = rows.Scan(&team, &n); e != nil {
				return Result{}, e
			}
			items = append(items, map[string]any{"team_id": team.String, "count": n})
		}
		return Result{Value: map[string]any{"items": items, "tallies": items}}, rows.Err()
	}
	if strings.Contains(a, "Timeline") {
		q := Str(v, "query")
		if target != "" {
			record, e := s.catalogRecord(ctx, user, "sports", target)
			if e != nil {
				return Result{}, e
			}
			q = Str(record, "hashtag", "name")
		}
		return s.timeline(ctx, Spec{Handler: "search", Action: "SearchTimeline"}, user, map[string]any{"raw_query": q, "count": limit(v)})
	}
	if target != "" {
		value, e := s.catalogRecord(ctx, user, "sports", target)
		return Result{Value: value}, e
	}
	items, e := s.listEntities(ctx, user, "sports", "", Str(v, "query"), true)
	if e != nil {
		return Result{}, e
	}
	out := []map[string]any{}
	lower := strings.ToLower(a)
	for _, item := range items {
		typ := Str(item, "type")
		if strings.Contains(lower, "team") && typ != "team" {
			continue
		}
		if strings.Contains(lower, "schedule") && typ != "game" {
			continue
		}
		if strings.Contains(lower, "standings") && typ != "standing" {
			continue
		}
		out = append(out, item)
	}
	return Result{Value: map[string]any{"items": out, "teams": out, "games": out, "standings": out, "stats": out, "week": nil}}, nil
}
func contains(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
func (s *Store) utility(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	switch a {
	case "SupportedLanguages", "languageSupportedLanguagesQuery":
		items := []map[string]any{{"code": "en", "name": "English", "local_name": "English"}, {"code": "ru", "name": "Russian", "local_name": "Русский"}, {"code": "uk", "name": "Ukrainian", "local_name": "Українська"}, {"code": "ro", "name": "Romanian", "local_name": "Română"}}
		return Result{Value: map[string]any{"languages": items, "items": items}}, nil
	case "UserSessionsList":
		if s.Cache == nil {
			return Result{}, fail(503, "session store unavailable")
		}
		keys, e := s.Cache.Keys(ctx, "session:*").Result()
		if e != nil {
			return Result{}, e
		}
		items := []map[string]any{}
		for _, key := range keys {
			owner, err := s.Cache.Get(ctx, key).Result()
			if err == nil && owner == user {
				ttl, _ := s.Cache.TTL(ctx, key).Result()
				items = append(items, map[string]any{"id": key[8:20], "expires_at": time.Now().Add(ttl), "active": true})
			}
		}
		return Result{Value: map[string]any{"sessions": items, "items": items}}, nil
	case "GetUserClaims":
		state, e := s.profileState(ctx, user)
		if e != nil {
			return Result{}, e
		}
		claims := []string{}
		if state["premium"] == true {
			claims = append(claims, "premium_standard")
		}
		return Result{Value: map[string]any{"claims": claims, "is_blue_verified": state["verified"]}}, nil
	case "PremiumHubQuery", "premiumBusinessLandingQuery":
		state, e := s.profileState(ctx, user)
		if e != nil {
			return Result{}, e
		}
		return Result{UserID: user, Value: map[string]any{"premium": state["premium"], "is_blue_verified": state["verified"], "subscriptions": []any{}, "managed_by": "telegram"}}, nil
	case "verifiedApplicationFormMutation":
		value, e := s.createEntity(ctx, user, "verification_application", "", v, false)
		return Result{Value: value}, e
	case "verifiedApplicationAccessQuery", "verifiedApplicationFormProfileQuery":
		state, e := s.profileState(ctx, user)
		if e != nil {
			return Result{}, e
		}
		return Result{UserID: user, Value: map[string]any{"eligible": true, "verified": state["verified"], "user_results": map[string]any{"result": map[string]any{"__user_id": user}}}}, nil
	case "UserUnderTheHoodReport", "AboutAccountQuery":
		id, e := s.targetUser(ctx, user, v)
		if e != nil {
			return Result{}, e
		}
		var created time.Time
		if e = s.DB.QueryRowContext(ctx, `SELECT created_at FROM users WHERE id=$1::bigint`, id).Scan(&created); e != nil {
			return Result{}, e
		}
		return Result{UserID: id, Value: map[string]any{"created_at": created, "username_changes": 0, "account_based_in": "FlashLarry", "id_verified": false}}, nil
	case "GenerateDrmToken":
		id, e := require(v, "mediaId", "media_id")
		if e != nil {
			return Result{}, e
		}
		var owner string
		if e = s.DB.QueryRowContext(ctx, `SELECT owner_id::text FROM media WHERE id::text=$1 AND status='ready'`, id).Scan(&owner); e != nil {
			return Result{}, fail(404, "media not found")
		}
		if owner != user {
			return Result{}, fail(403, "media belongs to another user")
		}
		key := []byte("flashlarry-playback-token")
		if s.Cache != nil {
			value, e := s.Cache.Get(ctx, "fl:media-signing-key").Bytes()
			if e != nil {
				return Result{}, fail(503, "media signing is not configured")
			}
			key = value
		}
		payload := Encode(map[string]any{"media_id": id, "user_id": user, "exp": time.Now().Add(10 * time.Minute).Unix()})
		mac := hmac.New(sha256.New, key)
		mac.Write(payload)
		token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
		return Result{Value: map[string]any{"token": token, "expires_in": 600, "provider": "flashlarry"}}, nil
	case "composerCardPreviewQuery":
		link := Str(v, "url")
		return Result{Value: map[string]any{"url": link, "card": nil}}, nil
	case "contentPageQuery":
		return s.timeline(ctx, Spec{Handler: "timeline", Action: "UserTweets"}, user, v)
	case "PeopleTitleQuery", "followersTitleQuery", "followingTitleQuery", "verifiedFollowersTitleQuery", "listsTitleQuery":
		id, e := s.targetUser(ctx, user, v)
		return Result{UserIDs: []string{id}, UserID: id}, e
	case "accountSwitchSlotUsersQuery":
		return Result{UserIDs: []string{user}}, nil
	case "delegateAccountsQuery", "affiliatesQuery":
		items, e := s.listEntities(ctx, user, "delegate", "", "", false)
		return Result{Value: map[string]any{"items": items, "users": items}}, e
	case "BakeryQuery", "UrtFixtures":
		return s.timeline(ctx, Spec{Handler: "timeline", Action: "HomeTimeline"}, user, v)
	case "Upsells", "voCardsQuery", "wallOfLoveCardQuery":
		return Result{Value: map[string]any{"items": []any{}, "eligible": false}}, nil
	}
	if strings.HasPrefix(a, "smartTag") {
		return s.catalog(ctx, Spec{Kind: spec.Kind, Action: a, Entity: "sports"}, user, v)
	}
	state, e := s.profileState(ctx, user)
	return Result{UserID: user, Value: state}, e
}
