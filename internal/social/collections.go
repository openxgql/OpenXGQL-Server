package social

import (
	"context"
	"database/sql"
	"strings"
)

func collectionID(v map[string]any, kind string) string {
	return Str(v, kind+"Id", kind+"_id", "restId", "rest_id", "id")
}
func (s *Store) lists(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	id := collectionID(v, "list")
	switch a {
	case "CreateList":
		name, e := require(v, "name", "listName", "list_name")
		if e != nil {
			return Result{}, e
		}
		if len([]rune(name)) > 100 {
			return Result{}, fail(400, "list name too long")
		}
		v["name"] = name
		v["description"] = Str(v, "description")
		private := Bool(v, "isPrivate") || Bool(v, "is_private") || Str(v, "mode") == "private"
		value, e := s.createEntity(ctx, user, "list", "", v, !private)
		return Result{Value: value}, e
	case "UpdateList", "DeleteList", "EditListBanner", "DeleteListBanner":
		if a == "DeleteListBanner" {
			v = map[string]any{"banner_media_id": nil}
		}
		value, e := s.updateEntity(ctx, user, "list", id, v, a == "DeleteList")
		return Result{Value: value, Scalar: "Done"}, e
	case "ListByRestId", "ListBySlug":
		if a == "ListBySlug" {
			slug := Str(v, "slug")
			var e error
			e = s.DB.QueryRowContext(ctx, `SELECT id::text FROM fl_entities WHERE kind='list' AND data->>'slug'=$1 AND deleted_at IS NULL AND (owner_id=$2::bigint OR public) LIMIT 1`, slug, user).Scan(&id)
			if e == sql.ErrNoRows {
				return Result{}, fail(404, "list not found")
			}
			if e != nil {
				return Result{}, e
			}
		}
		value, e := s.getEntity(ctx, user, "list", id, true)
		if e == nil {
			s.listCounts(ctx, id, user, value)
		}
		return Result{Value: value}, e
	case "ListAddMember", "ListRemoveMember":
		if _, e := s.getEntity(ctx, user, "list", id, false); e != nil {
			return Result{}, e
		}
		target, e := require(v, "userId", "user_id", "targetUserId", "target_user_id")
		if e != nil {
			return Result{}, e
		}
		if e = s.exists(ctx, "users", target); e != nil {
			return Result{}, e
		}
		if a == "ListAddMember" {
			e = s.putRelation(ctx, target, "list_member", id, map[string]any{"added_by": user})
		} else {
			e = s.deleteRelation(ctx, target, "list_member", id)
		}
		value, err := s.getEntity(ctx, user, "list", id, false)
		if e != nil {
			return Result{}, e
		}
		return Result{Value: value}, err
	case "ListSubscribe", "ListUnsubscribe", "MuteList", "UnmuteList":
		if _, e := s.getEntity(ctx, user, "list", id, true); e != nil {
			return Result{}, e
		}
		kind := "list_subscriber"
		if strings.Contains(a, "Mute") {
			kind = "list_mute"
		}
		var e error
		if a == "ListSubscribe" || a == "MuteList" {
			e = s.putRelation(ctx, user, kind, id, v)
		} else {
			e = s.deleteRelation(ctx, user, kind, id)
		}
		value, err := s.getEntity(ctx, user, "list", id, true)
		if e != nil {
			return Result{}, e
		}
		s.listCounts(ctx, id, user, value)
		return Result{Value: value}, err
	case "ListMembers", "ListSubscribers":
		value, e := s.getEntity(ctx, user, "list", id, true)
		if e != nil {
			return Result{}, e
		}
		kind := "list_member"
		if a == "ListSubscribers" {
			kind = "list_subscriber"
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT owner_id::text FROM fl_relations WHERE kind=$1 AND target_id=$2 ORDER BY created_at DESC LIMIT $3`, kind, id, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{UserIDs: ids, Timeline: true, Value: value}, e
	case "ListLatestTweetsTimeline", "ListRankedTweetsTimeline":
		value, e := s.getEntity(ctx, user, "list", id, true)
		if e != nil {
			return Result{}, e
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text FROM tweets t JOIN users u ON u.id=t.author_id WHERE `+visibility+` AND EXISTS(SELECT 1 FROM fl_relations r WHERE r.kind='list_member' AND r.target_id=$2 AND r.owner_id=t.author_id) ORDER BY t.id DESC LIMIT $3`, user, id, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{TweetIDs: ids, Timeline: true, Value: value}, e
	case "ListCreationRecommendedUsers", "ListEditRecommendedUsers":
		return s.timeline(ctx, Spec{Handler: "discovery", Action: "SidebarUserRecommendations"}, user, v)
	default:
		target, e := s.targetUser(ctx, user, v)
		if e != nil {
			return Result{}, e
		}
		q := Str(v, "query", "rawQuery", "raw_query")
		items, e := s.listEntities(ctx, user, "list", "", q, true)
		if a == "ListOwnerships" || a == "CombinedLists" || a == "ListsManagementPageTimeline" {
			filtered := []map[string]any{}
			for _, item := range items {
				if Str(item, "owner_id") == target {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		if e != nil {
			return Result{}, e
		}
		if a == "ListMemberships" || a == "CombinedLists" || a == "ListsManagementPageTimeline" {
			kind := "list_member"
			if a != "ListMemberships" {
				kind = "list_subscriber"
			}
			ids, e := s.relationIDs(ctx, target, kind)
			if e != nil {
				return Result{}, e
			}
			if a == "ListMemberships" {
				items = []map[string]any{}
			}
			seen := map[string]bool{}
			for _, item := range items {
				seen[Str(item, "id")] = true
			}
			for _, x := range ids {
				item, e := s.getEntity(ctx, user, "list", x, true)
				if e == nil && !seen[x] {
					items = append(items, item)
					seen[x] = true
				}
			}
		}
		for _, item := range items {
			s.listCounts(ctx, Str(item, "id"), user, item)
		}
		return Result{Value: map[string]any{"items": items, "items_results": items, "lists": items}, UserID: target}, nil
	}
}
func (s *Store) listCounts(ctx context.Context, id, user string, v map[string]any) {
	var members, subscribers int
	var followed bool
	_ = s.DB.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE kind='list_member'),count(*) FILTER(WHERE kind='list_subscriber'),COALESCE(bool_or(owner_id=$2::bigint AND kind='list_subscriber'),false) FROM fl_relations WHERE target_id=$1 AND kind IN ('list_member','list_subscriber')`, id, user).Scan(&members, &subscribers, &followed)
	v["member_count"] = members
	v["subscriber_count"] = subscribers
	v["following"] = followed
	v["is_member"] = false
}
func (s *Store) communities(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	id := collectionID(v, "community")
	switch a {
	case "CreateCommunity":
		name, e := require(v, "name")
		if e != nil {
			return Result{}, e
		}
		if len([]rune(name)) > 100 {
			return Result{}, fail(400, "community name too long")
		}
		v["name"] = name
		v["description"] = Str(v, "purpose", "description")
		value, e := s.createEntity(ctx, user, "community", "", v, true)
		if e != nil {
			return Result{}, e
		}
		id = Str(value, "id")
		e = s.putRelation(ctx, user, "community_member", id, map[string]any{"role": "Admin"})
		return Result{Value: value}, e
	case "CommunityByRestId", "CommunityAboutTimeline", "CommunityRules":
		value, e := s.getEntity(ctx, user, "community", id, true)
		if e != nil {
			return Result{}, e
		}
		s.communityMeta(ctx, user, id, value)
		return Result{Value: value}, nil
	case "CommunityEditName", "CommunityEditPurpose", "CommunityEditQuestion", "CommunityEditBannerMedia", "CommunityRemoveBannerMedia":
		if a == "CommunityRemoveBannerMedia" {
			v = map[string]any{"banner_media_id": nil}
		}
		value, e := s.updateEntity(ctx, user, "community", id, v, false)
		return Result{Value: value}, e
	case "CommunityCreateRule", "CommunityEditRule", "CommunityRemoveRule", "CommunityReorderRules":
		if _, e := s.getEntity(ctx, user, "community", id, false); e != nil {
			return Result{}, e
		}
		rule := Str(v, "ruleId", "rule_id")
		var e error
		var value map[string]any
		if a == "CommunityCreateRule" {
			if Str(v, "name", "description") == "" {
				return Result{}, fail(400, "rule content is required")
			}
			value, e = s.createEntity(ctx, user, "community_rule", id, v, true)
		} else if a == "CommunityReorderRules" {
			ids := stringsArray(v, "ruleIds", "rule_ids")
			for i, r := range ids {
				item, err := s.getEntity(ctx, user, "community_rule", r, false)
				if err != nil {
					return Result{}, err
				}
				var parent string
				if err = s.DB.QueryRowContext(ctx, `SELECT parent_id FROM fl_entities WHERE id::text=$1`, r).Scan(&parent); err != nil || parent != id {
					return Result{}, fail(400, "rule belongs to another community")
				}
				_ = item
				_, e = s.updateEntity(ctx, user, "community_rule", r, map[string]any{"position": i}, false)
				if e != nil {
					return Result{}, e
				}
			}
			value, e = s.getEntity(ctx, user, "community", id, true)
		} else {
			var parent string
			if err := s.DB.QueryRowContext(ctx, `SELECT parent_id FROM fl_entities WHERE id::text=$1 AND kind='community_rule'`, rule).Scan(&parent); err != nil || parent != id {
				return Result{}, fail(404, "rule not found in this community")
			}
			value, e = s.updateEntity(ctx, user, "community_rule", rule, v, a == "CommunityRemoveRule")
		}
		return Result{Value: value, Scalar: "Done"}, e
	case "JoinCommunity", "LeaveCommunity", "RequestToJoinCommunity":
		value, e := s.getEntity(ctx, user, "community", id, true)
		if e != nil {
			return Result{}, e
		}
		kind := "community_member"
		if a == "RequestToJoinCommunity" || Str(value, "membership_type") == "Restricted" {
			kind = "community_request"
		}
		if a == "LeaveCommunity" {
			if Str(value, "owner_id") == user {
				return Result{}, fail(400, "owner cannot leave the community")
			}
			e = s.deleteRelation(ctx, user, "community_member", id)
			if e == nil {
				e = s.deleteRelation(ctx, user, "community_request", id)
			}
		} else {
			e = s.putRelation(ctx, user, kind, id, map[string]any{"role": "Member"})
		}
		if e != nil {
			return Result{}, e
		}
		s.communityMeta(ctx, user, id, value)
		return Result{Value: value}, nil
	case "CommunityUpdateRole", "CommunityUserInvite":
		if _, e := s.getEntity(ctx, user, "community", id, false); e != nil {
			return Result{}, e
		}
		target, e := require(v, "userId", "user_id", "targetUserId", "target_user_id")
		if e != nil {
			return Result{}, e
		}
		if e = s.exists(ctx, "users", target); e != nil {
			return Result{}, e
		}
		role := Str(v, "role")
		if role == "" {
			role = "Member"
		}
		if role != "Member" && role != "Moderator" {
			return Result{}, fail(400, "invalid community role")
		}
		kind := "community_member"
		if a == "CommunityUserInvite" {
			kind = "community_invite"
		}
		e = s.putRelation(ctx, target, kind, id, map[string]any{"role": role})
		value, err := s.getEntity(ctx, user, "community", id, true)
		if e != nil {
			return Result{}, e
		}
		return Result{Value: value}, err
	case "CommunityMembers", "CommunityModerators", "CommunityMemberRelationshipTypeahead", "CommunityUserRelationshipTypeahead":
		value, e := s.getEntity(ctx, user, "community", id, true)
		if e != nil {
			return Result{}, e
		}
		q := Str(v, "query")
		role := ""
		if a == "CommunityModerators" {
			role = "Moderator"
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT r.owner_id::text FROM fl_relations r JOIN users u ON u.id=r.owner_id WHERE r.kind='community_member' AND r.target_id=$1 AND ($2='' OR r.data->>'role' IN ($2,'Admin')) AND ($3='' OR u.screen_name ILIKE '%'||$3||'%') ORDER BY r.created_at DESC LIMIT $4`, id, role, q, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{UserIDs: ids, Value: value}, e
	case "CommunityModerationKeepTweet":
		if _, e := s.getEntity(ctx, user, "community", id, false); e != nil {
			return Result{}, e
		}
		tweet, e := require(v, "tweetId", "tweet_id")
		if e != nil {
			return Result{}, e
		}
		e = s.deleteRelation(ctx, user, "community_hidden", tweet)
		return Result{Value: map[string]any{"success": e == nil}}, e
	case "CommunityModerationTweetCasesSlice", "CommunityTweetModerationLogSlice":
		if _, e := s.getEntity(ctx, user, "community", id, false); e != nil {
			return Result{}, e
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT target_id FROM fl_relations WHERE owner_id=$1::bigint AND kind='community_hidden' ORDER BY created_at DESC LIMIT $2`, user, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{TweetIDs: ids}, e
	}
	if strings.Contains(a, "Tweets") || strings.Contains(a, "Media") || strings.Contains(a, "Hashtags") {
		value, e := s.getEntity(ctx, user, "community", id, true)
		if e != nil {
			return Result{}, e
		}
		filter := ""
		if strings.Contains(a, "Media") {
			filter = ` AND EXISTS(SELECT 1 FROM tweet_media tm WHERE tm.tweet_id=t.id)`
		}
		rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text FROM tweets t JOIN users u ON u.id=t.author_id WHERE `+visibility+` AND EXISTS(SELECT 1 FROM fl_relations r WHERE r.kind='community_post' AND r.target_id=t.id::text AND r.data->>'community_id'=$2)`+filter+` ORDER BY t.id DESC LIMIT $3`, user, id, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{TweetIDs: ids, Timeline: true, Value: value}, e
	}
	items, e := s.listEntities(ctx, user, "community", "", Str(v, "query", "raw_query"), true)
	if e != nil {
		return Result{}, e
	}
	if strings.Contains(a, "Membership") {
		ids, e := s.relationIDs(ctx, user, "community_member")
		if e != nil {
			return Result{}, e
		}
		items = []map[string]any{}
		for _, id := range ids {
			item, e := s.getEntity(ctx, user, "community", id, true)
			if e == nil {
				items = append(items, item)
			}
		}
	}
	for _, item := range items {
		s.communityMeta(ctx, user, Str(item, "id"), item)
	}
	return Result{Value: map[string]any{"items": items, "items_results": items, "communities": items}}, nil
}
func (s *Store) communityMeta(ctx context.Context, user, id string, v map[string]any) {
	rules, _ := s.listEntities(ctx, user, "community_rule", id, "", true)
	v["rules"] = rules
	var count int
	_ = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM fl_relations WHERE kind='community_member' AND target_id=$1`, id).Scan(&count)
	v["member_count"] = count
	var role string
	_ = s.DB.QueryRowContext(ctx, `SELECT data->>'role' FROM fl_relations WHERE kind='community_member' AND target_id=$1 AND owner_id=$2::bigint`, id, user).Scan(&role)
	v["role"] = role
	v["is_member"] = role != ""
	v["join_policy"] = Str(v, "membership_type")
	if v["join_policy"] == "" {
		v["join_policy"] = "Open"
	}
}
