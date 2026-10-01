package main

import (
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
)

func timelineUserEntry(user User, index string) map[string]any {
	if len(index) < 20 {
		index = strings.Repeat("0", 20-len(index)) + index
	}
	profile := xUser(user)
	profile["id"] = base64.StdEncoding.EncodeToString([]byte("User:" + user.ID))
	results := map[string]any{"id": "user-results-" + user.ID, "result": profile}
	return map[string]any{
		"entryId": "user-" + user.ID, "sortIndex": index,
		"entry_id": "user-" + user.ID, "sort_index": index,
		"content": map[string]any{
			"__typename": "TimelineTimelineItem", "entryType": "TimelineTimelineItem",
			"content":     map[string]any{"__typename": "TimelineUser", "display_type": "User", "user_display_type": "User", "userDisplayType": "User", "user_results": results},
			"itemContent": map[string]any{"__typename": "TimelineUser", "itemType": "TimelineUser", "display_type": "User", "user_display_type": "User", "userDisplayType": "User", "user_results": results},
		},
	}
}

func (g *Gateway) webFollowMutation(w http.ResponseWriter, r *http.Request, vars map[string]any, follow bool) {
	targetID, err := mustString(vars, "targetUserId")
	if err != nil {
		graphqlError(w, 400, err.Error())
		return
	}
	if n, err := strconv.ParseInt(targetID, 10, 64); err != nil || n <= 0 {
		graphqlError(w, 400, "invalid targetUserId")
		return
	}
	actorID := actorFrom(r.Context()).UserID
	method := http.MethodPut
	field := "follow_user"
	resultType := "FollowSuccessResult"
	if !follow {
		method = http.MethodDelete
		field = "unfollow_user"
		resultType = "UnfollowSuccessResult"
	}
	endpoint := g.usersURL + "/follows/" + url.PathEscape(targetID) + "/followers/" + url.PathEscape(actorID)
	if err := g.call(r.Context(), method, endpoint, nil, nil); err != nil {
		writeAPIError(w, err)
		return
	}
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{field: map[string]any{"__typename": resultType}}})
}

func (g *Gateway) webFollowList(w http.ResponseWriter, r *http.Request, vars map[string]any, kind string) {
	name, err := mustString(vars, "screenName")
	if err != nil {
		graphqlError(w, 400, err.Error())
		return
	}
	profile, err := g.userByName(r.Context(), name)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	endpoint := g.usersURL + "/follows/" + url.PathEscape(profile.ID) + "/" + kind + "?limit=" + strconv.Itoa(count(vars))
	if cursor, _ := vars["cursor"].(string); cursor != "" {
		endpoint += "&cursor=" + url.QueryEscape(cursor)
	}
	var page struct {
		Users []struct {
			FollowID string `json:"follow_id"`
			User     User   `json:"user"`
		} `json:"users"`
		NextCursor string `json:"next_cursor"`
	}
	if err := g.call(r.Context(), http.MethodGet, endpoint, nil, &page); err != nil {
		writeAPIError(w, err)
		return
	}
	entries := make([]map[string]any, 0, len(page.Users)+1)
	for _, item := range page.Users {
		if err := g.enrichUser(r.Context(), &item.User); err != nil {
			writeAPIError(w, err)
			return
		}
		entries = append(entries, timelineUserEntry(item.User, item.FollowID))
	}
	if page.NextCursor != "" {
		entries = append(entries, map[string]any{
			"entryId":   "cursor-bottom-" + page.NextCursor,
			"sortIndex": page.NextCursor,
			"content": map[string]any{"__typename": "TimelineTimelineCursor", "entryType": "TimelineTimelineCursor",
				"cursor_type": "Bottom", "cursorType": "Bottom", "value": page.NextCursor},
		})
	}
	id := "user-" + profile.ID + "-" + kind
	instructions := []map[string]any{{"type": "TimelineAddEntries", "__typename": "TimelineAddEntries", "entries": entries}}
	field := kind + "_timeline"
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{
		"user_result_by_screen_name": map[string]any{"result": map[string]any{
			"__typename": "User", "rest_id": profile.ID,
			field: map[string]any{"id": id, "timeline": map[string]any{"id": id, "instructions": instructions}},
		}},
	}})
}
