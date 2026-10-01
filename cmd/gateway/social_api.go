package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/common"
	"flashlarry-api/internal/social"
)

func viewerID(ctx context.Context) string {
	if a := actorFrom(ctx); a != nil {
		return a.UserID
	}
	return ""
}
func (g *Gateway) visibleIDs(ctx context.Context, ids []string) ([]string, error) {
	if g.socialURL == "" {
		return ids, nil
	}
	out := []string{}
	err := g.call(ctx, "POST", g.socialURL+"/visibility", map[string]any{"user_id": viewerID(ctx), "ids": ids}, &out)
	return out, err
}
func (g *Gateway) checkPostAccess(ctx context.Context, id string) error {
	if g.socialURL == "" {
		return nil
	}
	ids, e := g.visibleIDs(ctx, []string{id})
	if e != nil {
		return e
	}
	if len(ids) == 0 {
		return &APIError{404, "post unavailable"}
	}
	return nil
}
func (g *Gateway) visibleTweets(ctx context.Context, tweets []Tweet) ([]Tweet, error) {
	if g.socialURL == "" || len(tweets) == 0 {
		return tweets, nil
	}
	ids := []string{}
	for _, t := range tweets {
		ids = append(ids, t.ID)
	}
	allowed, e := g.visibleIDs(ctx, ids)
	if e != nil {
		return nil, e
	}
	set := map[string]bool{}
	for _, id := range allowed {
		set[id] = true
	}
	out := []Tweet{}
	for _, t := range tweets {
		if set[t.ID] {
			out = append(out, t)
		}
	}
	return out, nil
}
func normalizedVariables(input map[string]any) map[string]any {
	v := map[string]any{}
	for k, x := range input {
		v[k] = x
	}
	for _, k := range []string{"input", "request", "data", "post_tweet_request"} {
		if x, ok := v[k].(map[string]any); ok {
			for a, b := range x {
				if _, exists := v[a]; !exists {
					v[a] = b
				}
			}
		}
	}
	for dst, srcs := range map[string][]string{"screen_name": {"screenName", "username"}, "tweet_id": {"tweetId", "restId", "source_tweet_id"}, "tweetId": {"tweet_id", "restId", "focalTweetId"}, "userId": {"user_id", "targetUserId", "restId"}, "tweet_text": {"text", "tweetText"}} {
		if _, ok := v[dst]; !ok {
			for _, src := range srcs {
				if x, ok := v[src]; ok {
					v[dst] = x
					break
				}
			}
		}
	}
	if attachment := social.Str(v, "attachment_url"); strings.Contains(attachment, "/status/") {
		parts := strings.Split(attachment, "/status/")
		id := strings.Split(parts[len(parts)-1], "?")[0]
		if _, e := strconv.ParseInt(id, 10, 64); e == nil {
			v["quoted_tweet_id"] = id
		}
	}
	return v
}
func writeGraphQLError(w http.ResponseWriter, e error) {
	var api *APIError
	if errors.As(e, &api) {
		graphqlError(w, api.Status, api.Message)
	} else {
		graphqlError(w, 500, "operation failed")
	}
}
func (g *Gateway) registryGraphQL(w http.ResponseWriter, r *http.Request, spec social.Spec) {
	if spec.Handler == "excluded" {
		graphqlError(w, 404, "feature excluded: "+spec.Action)
		return
	}
	if spec.Kind == "mutation" && r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		graphqlError(w, 405, "method not allowed")
		return
	}
	v, ok := requestVariables(w, r)
	if !ok {
		return
	}
	v = normalizedVariables(v)
	if spec.Handler == "core" {
		result, e := g.executeInternal(r, spec.Action, v)
		if e != nil {
			writeGraphQLError(w, e)
			return
		}
		// Documented aliases retain the response of the canonical operation.
		common.WriteJSON(w, 200, map[string]any{"data": result})
		return
	}
	var result social.Result
	e := g.call(r.Context(), "POST", g.socialURL+"/execute", map[string]any{"name": spec.Name, "id": spec.ID, "user_id": viewerID(r.Context()), "variables": v}, &result)
	if e != nil {
		writeGraphQLError(w, e)
		return
	}
	data, e := g.socialResponse(r.Context(), spec, result)
	if e != nil {
		writeGraphQLError(w, e)
		return
	}
	common.WriteJSON(w, 200, map[string]any{"data": data})
}
func (g *Gateway) resolveSocialValue(ctx context.Context, x any) (any, error) {
	switch v := x.(type) {
	case map[string]any:
		if id := social.Str(v, "__tweet_id"); id != "" {
			t, e := g.tweetByID(ctx, id)
			if e != nil {
				var api *APIError
				if errors.As(e, &api) && (api.Status == 403 || api.Status == 404) {
					return nil, nil
				}
				return nil, e
			}
			return g.xTweet(ctx, t)
		}
		if id := social.Str(v, "__user_id"); id != "" {
			u, e := g.userByID(ctx, id)
			if e != nil {
				return nil, e
			}
			return xUser(u), nil
		}
		for k, item := range v {
			value, e := g.resolveSocialValue(ctx, item)
			if e != nil {
				return nil, e
			}
			v[k] = value
		}
		return v, nil
	case []any:
		for i, item := range v {
			value, e := g.resolveSocialValue(ctx, item)
			if e != nil {
				return nil, e
			}
			v[i] = value
		}
		return v, nil
	case []map[string]any:
		for i, item := range v {
			value, e := g.resolveSocialValue(ctx, item)
			if e != nil {
				return nil, e
			}
			v[i] = value.(map[string]any)
		}
		return v, nil
	}
	return x, nil
}

type socialView struct {
	value         map[string]any
	user          map[string]any
	users, tweets []any
	timeline      map[string]any
	scalar        any
}

func objectMap(x any) map[string]any { m, _ := x.(map[string]any); return m }
func copyMap(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
func (g *Gateway) socialResponse(ctx context.Context, spec social.Spec, result social.Result) (map[string]any, error) {
	value, e := g.resolveSocialValue(ctx, result.Value)
	if e != nil {
		return nil, e
	}
	v := socialView{value: objectMap(value), scalar: result.Scalar, users: []any{}, tweets: []any{}}
	if v.value == nil {
		v.value = map[string]any{}
	}
	id := result.UserID
	if id == "" {
		id = viewerID(ctx)
	}
	if id != "" {
		u, e := g.userByID(ctx, id)
		if e != nil {
			return nil, e
		}
		v.user = xUser(u)
	}
	for _, id := range result.UserIDs {
		u, e := g.userByID(ctx, id)
		if e != nil {
			var api *APIError
			if errors.As(e, &api) && (api.Status == 404 || api.Status == 403) {
				continue
			}
			return nil, e
		}
		v.users = append(v.users, xUser(u))
	}
	ids, e := g.visibleIDs(ctx, result.TweetIDs)
	if e != nil {
		return nil, e
	}
	posts := []Tweet{}
	for _, id := range ids {
		var t Tweet
		if e = g.call(ctx, "GET", g.tweetsURL+"/tweets/"+url.PathEscape(id), nil, &t); e != nil {
			return nil, e
		}
		posts = append(posts, t)
		x, e := g.xTweet(ctx, t)
		if e != nil {
			return nil, e
		}
		v.tweets = append(v.tweets, x)
	}
	instructions, e := g.instructions(ctx, posts)
	if e != nil {
		return nil, e
	}
	if len(v.users) > 0 {
		entries := []any{}
		for _, x := range v.users {
			u := objectMap(x)
			id := social.Str(u, "rest_id")
			content := map[string]any{"__typename": "TimelineUser", "itemType": "TimelineUser", "user_results": map[string]any{"rest_id": id, "result": u}, "userDisplayType": "User"}
			entries = append(entries, map[string]any{"entry_id": "user-" + id, "entryId": "user-" + id, "sort_index": id, "sortIndex": id, "content": map[string]any{"__typename": "TimelineTimelineItem", "entryType": "TimelineTimelineItem", "content": content, "itemContent": content}})
		}
		instructions = append(instructions, map[string]any{"__typename": "TimelineAddEntries", "type": "TimelineAddEntries", "entries": entries})
	}
	if result.Cursor != "" {
		content := map[string]any{"__typename": "TimelineTimelineCursor", "entryType": "TimelineTimelineCursor", "value": result.Cursor, "cursor_type": "Bottom", "cursorType": "Bottom"}
		instructions = append(instructions, map[string]any{"__typename": "TimelineAddEntries", "type": "TimelineAddEntries", "entries": []any{map[string]any{"entryId": "cursor-bottom", "entry_id": "cursor-bottom", "sortIndex": "0", "sort_index": "0", "content": content}}})
	}
	if spec.Handler == "discovery" && spec.Action == "ExploreSidebar" {
		instructions = sidebarInstructions(v.users, v.value)
	}
	v.timeline = map[string]any{"id": "flashlarry-" + spec.Name + "-" + id, "timeline": map[string]any{"instructions": instructions}, "instructions": instructions}
	if spec.Handler == "notification" && spec.Action == "NotificationsTimeline" {
		v.timeline["timeline"] = map[string]any{"instructions": notificationInstructions(v.value)}
		v.timeline["instructions"] = notificationInstructions(v.value)
	}
	if spec.Web {
		return v.project(spec.Selections, map[string]any{}, true), nil
	}
	root := ""
	if len(spec.Roots) > 0 {
		root = spec.Roots[0]
	}
	if root == "" {
		return nil, fmt.Errorf("operation has no response root")
	}
	out := any(v.value)
	switch spec.Handler {
	case "bookmark":
		if result.Timeline {
			out = v.timeline
		} else if result.Scalar != nil {
			out = result.Scalar
		}
	case "edge":
		if spec.Entity == "bookmark" {
			out = result.Scalar
		}
	case "follow_list", "relationship_list", "reaction_users", "timeline":
		if strings.HasPrefix(spec.Action, "User") || spec.Handler == "follow_list" {
			u := copyMap(v.user)
			u["timeline_v2"] = v.timeline
			u["timeline"] = v.timeline
			out = map[string]any{"result": u}
			if spec.Handler == "follow_list" {
				out = map[string]any{"result": u}
			}
			root = "user"
		} else {
			out = v.timeline
		}
	case "lookup":
		if len(v.users) > 0 {
			wrapped := []any{}
			for _, u := range v.users {
				wrapped = append(wrapped, map[string]any{"result": u})
			}
			if len(wrapped) == 1 {
				out = wrapped[0]
			} else {
				out = wrapped
			}
		} else {
			wrapped := []any{}
			for _, t := range v.tweets {
				wrapped = append(wrapped, map[string]any{"result": t})
			}
			if len(wrapped) == 1 {
				out = wrapped[0]
			} else {
				out = wrapped
			}
		}
	case "list":
		if result.Timeline {
			base := copyMap(v.value)
			base["tweets_timeline"] = v.timeline
			base["members_timeline"] = v.timeline
			base["subscribers_timeline"] = v.timeline
			out = base
		}
	case "community":
		base := copyMap(v.value)
		if result.Timeline {
			base["ranked_community_timeline"] = v.timeline
			base["community_timeline"] = v.timeline
		}
		if spec.Action == "CommunityByRestId" {
			out = map[string]any{"result": base}
		} else {
			out = base
		}
	case "notification":
		if result.Timeline {
			u := copyMap(v.user)
			u["notification_timeline"] = v.timeline
			out = map[string]any{"user_results": map[string]any{"result": u}}
		}
	case "search", "discovery":
		base := copyMap(v.value)
		base["timeline"] = v.timeline
		out = base
	}
	return map[string]any{root: out}, nil
}
func (v socialView) project(fields []social.Field, base map[string]any, root bool) map[string]any {
	out := copyMap(base)
	for _, f := range fields {
		if f.Name == "" {
			for k, x := range v.project(f.Selections, base, false) {
				out[k] = x
			}
			continue
		}
		key := f.Name
		if f.Alias != "" {
			key = f.Alias
		}
		x, present := base[key]
		if !present {
			x, present = base[f.Name]
		}
		if !present {
			x, present = v.value[key]
		}
		if !present {
			x, present = v.value[f.Name]
		}
		if f.Kind == "ScalarField" {
			if !present {
				if f.Name == "__typename" {
					x = f.Type
					if x == "" {
						x = "Success"
					}
				} else if root && v.scalar != nil {
					x = v.scalar
				} else {
					x = nil
				}
			}
			out[key] = x
			continue
		}
		if f.Plural {
			if !present {
				switch f.Type {
				case "User":
					x = v.users
				case "UserResults":
					a := []any{}
					for _, u := range v.users {
						a = append(a, map[string]any{"result": u})
					}
					x = a
				case "Tweet":
					x = v.tweets
				case "TweetResults":
					a := []any{}
					for _, t := range v.tweets {
						a = append(a, map[string]any{"result": t})
					}
					x = a
				default:
					x = v.value["items"]
					if x == nil {
						x = []any{}
					}
				}
			}
			raw, _ := json.Marshal(x)
			var items []any
			_ = json.Unmarshal(raw, &items)
			output := []any{}
			for _, item := range items {
				if m := objectMap(item); m != nil {
					output = append(output, v.project(f.Selections, m, false))
				} else {
					output = append(output, item)
				}
			}
			out[key] = output
			continue
		}
		var child map[string]any
		if present {
			if x == nil {
				out[key] = nil
				continue
			}
			child = objectMap(x)
		}
		if child == nil {
			switch {
			case f.Type == "Timeline" || f.Type == "TimelineTimeline" || strings.Contains(f.Name, "timeline"):
				child = copyMap(v.timeline)
				if f.Type == "TimelineTimeline" {
					child = copyMap(objectMap(v.timeline["timeline"]))
				}
			case f.Type == "Viewer" || f.Name == "viewer" || f.Name == "viewer_v2":
				child = copyMap(v.value)
				child["user_results"] = map[string]any{"result": v.user}
				child["__typename"] = "Viewer"
			case f.Type == "UserResults" || f.Name == "user_result_by_screen_name" || f.Name == "user_result_by_rest_id":
				u := v.user
				if len(v.users) > 0 {
					u = objectMap(v.users[0])
				}
				child = map[string]any{"result": u, "rest_id": u["rest_id"]}
			case f.Type == "TweetResults" || strings.Contains(f.Name, "tweet_result"):
				var t any
				if len(v.tweets) > 0 {
					t = v.tweets[0]
				}
				child = map[string]any{"result": t}
			case f.Name == "result":
				switch {
				case len(v.tweets) > 0:
					child = copyMap(objectMap(v.tweets[0]))
				case len(v.users) > 0:
					child = copyMap(objectMap(v.users[0]))
				case f.Type == "User":
					child = copyMap(v.user)
				default:
					child = copyMap(v.value)
				}
			case f.Type == "CommunityResults":
				child = map[string]any{"result": v.value}
			case root:
				child = copyMap(v.value)
			case strings.HasSuffix(f.Type, "Slice") || strings.HasSuffix(f.Name, "slice"):
				child = map[string]any{"items": v.value["items"], "items_results": v.value["items"]}
			default:
				out[key] = nil
				continue
			}
		}
		if f.Type != "" {
			if _, ok := child["__typename"]; !ok {
				child["__typename"] = f.Type
			}
		}
		out[key] = v.project(f.Selections, child, false)
	}
	return out
}
func (g *Gateway) reportSpam(w http.ResponseWriter, r *http.Request) {
	if e := r.ParseForm(); e != nil {
		common.Error(w, 400, "invalid form")
		return
	}
	id := r.Form.Get("user_id")
	if id == "" {
		u, e := g.userByName(r.Context(), r.Form.Get("screen_name"))
		if e != nil {
			writeAPIError(w, e)
			return
		}
		id = u.ID
	}
	var report any
	if e := g.call(r.Context(), "POST", g.socialURL+"/reports", map[string]any{"reporter_id": viewerID(r.Context()), "target_kind": "user", "target_id": id, "reason": "spam"}, &report); e != nil {
		writeAPIError(w, e)
		return
	}
	u, e := g.userByID(r.Context(), id)
	if e != nil {
		writeAPIError(w, e)
		return
	}
	common.WriteJSON(w, 200, xUser(u)["legacy"])
}
func (g *Gateway) reportPost(w http.ResponseWriter, r *http.Request) {
	var v map[string]any
	if !common.Decode(w, r, &v) {
		return
	}
	v["reporter_id"] = viewerID(r.Context())
	var report any
	if e := g.call(r.Context(), "POST", g.socialURL+"/reports", v, &report); e != nil {
		writeAPIError(w, e)
		return
	}
	common.WriteJSON(w, 201, report)
}
func (g *Gateway) adClick(w http.ResponseWriter, r *http.Request) {
	var v struct {
		URL string `json:"target_url"`
	}
	if e := g.call(r.Context(), "GET", g.socialURL+"/ads/click/"+url.PathEscape(r.PathValue("delivery")), nil, &v); e != nil {
		writeAPIError(w, e)
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, v.URL, http.StatusFound)
}

type quoteDepthKey struct{}

func socialString(v map[string]any, k string) string { return social.Str(v, k) }
func (g *Gateway) homeInstructions(ctx context.Context, tweets []Tweet) ([]map[string]any, error) {
	instructions, e := g.instructions(ctx, tweets)
	if e != nil || g.socialURL == "" {
		return instructions, e
	}
	var ad *social.Ad
	if e = g.call(ctx, "GET", g.socialURL+"/ads/next?viewer_id="+url.QueryEscape(viewerID(ctx)), nil, &ad); e != nil {
		return nil, e
	}
	if ad == nil {
		return instructions, nil
	}
	tweet, e := g.tweetByID(ctx, ad.TweetID)
	if e != nil {
		return nil, e
	}
	tracking := common.PublicBaseURL() + "/ads/click/" + ad.DeliveryID
	tweet.Text = "Реклама · " + tweet.Text + "\n" + tracking
	items, e := g.instructions(ctx, []Tweet{tweet})
	if e != nil {
		return nil, e
	}
	entry := items[0]["entries"].([]map[string]any)[0]
	content := objectMap(entry["content"])
	metadata := map[string]any{"advertiser_results": objectMap(objectMap(objectMap(objectMap(content["content"])["tweet_results"])["result"])["core"])["user_results"], "impression_string": ad.DeliveryID, "disclosure_type": "NoDisclosure", "ad_metadata_container": map[string]any{"is_quick_promote": false, "remove_promoted_attribution_for_preroll": false}}
	// Visible text marks every advertisement even when an upstream renderer omits attribution.
	objectMap(content["content"])["promoted_metadata"] = metadata
	objectMap(content["itemContent"])["promotedMetadata"] = metadata
	entries := instructions[0]["entries"].([]map[string]any)
	index := 3
	if len(entries) < index {
		index = len(entries)
	}
	merged := append([]map[string]any{}, entries[:index]...)
	merged = append(merged, entry)
	merged = append(merged, entries[index:]...)
	instructions[0]["entries"] = merged
	return instructions, nil
}

func notificationInstructions(value map[string]any) []map[string]any {
	entries := []any{}
	items, _ := value["items"].([]any)
	for _, raw := range items {
		n := objectMap(raw)
		u := objectMap(objectMap(n["user_results"])["result"])
		if u == nil {
			continue
		}
		core := objectMap(u["core"])
		kind := social.Str(n, "kind")
		suffix, icon := "подписался на вас", "Person"
		switch kind {
		case "like":
			suffix = "понравилась ваша запись"
			icon = "Heart"
		case "repost":
			suffix = "сделал репост вашей записи"
			icon = "Retweet"
		case "reply":
			suffix = "ответил на вашу запись"
			icon = "Reply"
		}
		text := social.Str(core, "name") + " " + suffix
		link := "/" + social.Str(core, "screen_name")
		if tweet := social.Str(n, "tweet_id"); tweet != "" {
			link = "/i/status/" + tweet
		}
		targets := []any{}
		if tr := objectMap(n["tweet_results"]); tr != nil && tr["result"] != nil {
			targets = append(targets, map[string]any{"__typename": "TimelineNotificationTweetRef", "tweet_results": tr})
		}
		content := map[string]any{"__typename": "TimelineNotification", "itemType": "TimelineNotification", "icon": icon, "message": map[string]any{"text": text}, "rich_message": map[string]any{"text": text, "entities": []any{}, "rtl": false, "alignment": "Natural"}, "url": map[string]any{"url": link, "url_type": "DeepLink"}, "template": map[string]any{"__typename": "TimelineNotificationAggregateUserActions", "from_users": []any{map[string]any{"__typename": "TimelineNotificationUserRef", "user_results": n["user_results"]}}, "target_objects": targets, "show_all_link_text": nil, "additional_context": nil}, "social_context": nil}
		if created, ok := n["created_at"].(string); ok {
			if t, e := time.Parse(time.RFC3339Nano, created); e == nil {
				content["timestamp_ms"] = t.UnixMilli()
			}
		}
		id := social.Str(n, "id")
		entries = append(entries, map[string]any{"entry_id": "notification-" + id, "entryId": "notification-" + id, "sort_index": id, "sortIndex": id, "content": map[string]any{"__typename": "TimelineTimelineItem", "entryType": "TimelineTimelineItem", "content": content, "itemContent": content}})
	}
	return []map[string]any{{"__typename": "TimelineAddEntries", "type": "TimelineAddEntries", "entries": entries}}
}
