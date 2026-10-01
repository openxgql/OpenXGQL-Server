package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"flashlarry-api/internal/social"
)

// ExploreSidebar uses the captured client's URT module, user and trend formats.
// It is independent of the Home/Explore post timelines.
func sidebarInstructions(users []any, value map[string]any) []map[string]any {
	entries := []any{}
	module := func(id, title, destination string, contents []map[string]any) {
		if len(contents) == 0 {
			return
		}
		items := []any{}
		for i, content := range contents {
			items = append(items, map[string]any{"entry_id": id + "-" + strconv.Itoa(i), "item": map[string]any{"__typename": "TimelineTimelineItem", "content": content}})
		}
		content := map[string]any{
			"__typename": "TimelineTimelineModule", "display_type": "Vertical", "items": items,
			"header": map[string]any{"display_type": "Classic", "text": title},
			"footer": map[string]any{"text": "Show more", "landing_url": map[string]any{"url_type": "DeepLink", "url": destination}},
		}
		sort := strconv.Itoa(100 - len(entries))
		entries = append(entries, map[string]any{"entry_id": id, "entryId": id, "sort_index": sort, "sortIndex": sort, "content": content})
	}
	people := []map[string]any{}
	for _, user := range users {
		people = append(people, map[string]any{"__typename": "TimelineUser", "user_display_type": "UserCompact", "display_type": "UserCompact", "user_results": map[string]any{"result": user}})
	}
	module("who-to-follow", "Who to follow", "twitter://connect_people", people)
	var trends []map[string]any
	raw, _ := json.Marshal(value["trends"])
	_ = json.Unmarshal(raw, &trends)
	topics := []map[string]any{}
	for _, trend := range trends {
		name := social.Str(trend, "name")
		if name == "" {
			continue
		}
		topics = append(topics, map[string]any{
			"__typename": "TimelineTrend", "name": name, "is_ai_trend": false,
			"url":            map[string]any{"url_type": "DeepLink", "url": "twitter://search?query=" + url.QueryEscape(name)},
			"trend_metadata": map[string]any{"domain_context": "Trending on FlashLarry", "meta_description": fmt.Sprint(trend["tweet_count"]) + " posts"},
		})
	}
	module("trends", "Trending", "twitter://explore", topics)
	return []map[string]any{
		{"__typename": "TimelineAddEntries", "type": "TimelineAddEntries", "entries": entries},
		{"__typename": "TimelineTerminateTimeline", "type": "TimelineTerminateTimeline", "direction": "Bottom"},
	}
}
