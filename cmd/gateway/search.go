package main

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"flashlarry-api/internal/common"
)

func (g *Gateway) searchUsers(ctx context.Context, query string, n int) ([]User, error) {
	var users []User
	endpoint := g.usersURL + "/users/search?q=" + url.QueryEscape(query) + "&limit=" + strconv.Itoa(n)
	err := g.call(ctx, http.MethodGet, endpoint, nil, &users)
	if err == nil {
		for i := range users {
			if err = g.enrichUser(ctx, &users[i]); err != nil {
				break
			}
		}
	}
	return users, err
}

func (g *Gateway) searchTweets(ctx context.Context, query, product string, n int) ([]Tweet, error) {
	var tweets []Tweet
	endpoint := g.tweetsURL + "/search?q=" + url.QueryEscape(query) + "&product=" + url.QueryEscape(product) + "&limit=" + strconv.Itoa(n)
	err := g.call(ctx, http.MethodGet, endpoint, nil, &tweets)
	return tweets, err
}

func searchQuery(vars map[string]any, name string) string {
	q, _ := vars[name].(string)
	q = strings.TrimSpace(q)
	if len([]rune(q)) > 200 {
		return ""
	}
	return q
}

func (g *Gateway) webSearchByRawQuery(w http.ResponseWriter, r *http.Request, vars map[string]any) {
	q := searchQuery(vars, "rawQuery")
	if q == "" {
		graphqlError(w, 400, "variables.rawQuery is required")
		return
	}
	product, _ := vars["product"].(string)
	var instructions []map[string]any
	var err error
	if strings.EqualFold(product, "People") {
		var users []User
		users, err = g.searchUsers(r.Context(), q, count(vars))
		if err == nil {
			instructions = userSearchInstructions(users)
		}
	} else {
		var tweets []Tweet
		tweets, err = g.searchTweets(r.Context(), q, product, count(vars))
		if err == nil {
			instructions, err = g.instructions(r.Context(), tweets)
		}
		if err == nil && (product == "" || strings.EqualFold(product, "Top")) {
			var users []User
			users, err = g.searchUsers(r.Context(), q, 5)
			if err == nil && len(users) > 0 {
				userEntries := userSearchInstructions(users)[0]["entries"].([]map[string]any)
				postEntries := instructions[0]["entries"].([]map[string]any)
				instructions[0]["entries"] = append(userEntries, postEntries...)
			}
		}
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	id := "search:" + strings.ToLower(product) + ":" + strings.ToLower(q)
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{
		"search_by_raw_query": map[string]any{"id": id,
			"search_timeline": map[string]any{"id": id, "timeline": map[string]any{"id": id, "instructions": instructions}},
		},
	}})
}

func userSearchInstructions(users []User) []map[string]any {
	entries := make([]map[string]any, 0, len(users))
	for _, user := range users {
		entries = append(entries, timelineUserEntry(user, user.ID))
	}
	return []map[string]any{{"type": "TimelineAddEntries", "__typename": "TimelineAddEntries", "entries": entries}}
}

func (g *Gateway) webSearchTypeahead(w http.ResponseWriter, r *http.Request, vars map[string]any) {
	q := searchQuery(vars, "query")
	items := make([]any, 0)
	if q != "" {
		items = append(items, map[string]any{"__typename": "TypeaheadQuerySuggestionItem", "query": q})
		users, err := g.searchUsers(r.Context(), q, 7)
		if err != nil {
			writeAPIError(w, err)
			return
		}
		for _, user := range users {
			items = append(items, map[string]any{"__typename": "TypeaheadUserItem", "user_results": map[string]any{"result": xUser(user)}})
		}
	}
	common.WriteJSON(w, 200, map[string]any{"data": map[string]any{
		"viewer": map[string]any{
			"search_search_by_raw_query": map[string]any{
				"global_search_typeahead": map[string]any{"__typename": "TypeaheadSlice", "items": items},
			},
		},
	}})
}
