package main

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"flashlarry-api/internal/social"
)

func TestExploreSidebarContainsRecommendationsInsteadOfPostFeed(t *testing.T) {
	spec, ok := social.Lookup("exploreSidebarQuery", "-PzUrrG_dtVeNv9_tyW6aA")
	if !ok {
		t.Fatal("captured sidebar query missing")
	}
	user := map[string]any{"__typename": "User", "rest_id": "42"}
	instructions := sidebarInstructions([]any{user}, map[string]any{"trends": []any{map[string]any{"name": "#Тест", "tweet_count": 2}}})
	v := socialView{value: map[string]any{}, timeline: map[string]any{"id": "sidebar", "timeline": map[string]any{"instructions": instructions}}}
	data := v.project(spec.Selections, map[string]any{}, true)
	// Exercise the actual serialized response selected by the official client.
	raw, _ := json.Marshal(data)
	if strings.Contains(string(raw), "TimelineTweet") || strings.Contains(string(raw), "tweet_results") {
		t.Fatal("sidebar contains a post feed", string(raw))
	}
	var decoded map[string]any
	if e := json.Unmarshal(raw, &decoded); e != nil {
		t.Fatal(e)
	}
	timeline := objectMap(objectMap(decoded["explore_sidebar"])["timeline"])
	instr := timeline["instructions"].([]any)
	entries := objectMap(instr[0])["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("wanted people and trends modules, got %s", raw)
	}
	for _, entry := range entries {
		module := objectMap(objectMap(entry)["content"])
		if module["__typename"] != "TimelineTimelineModule" || module["display_type"] != "Vertical" {
			t.Fatal("unsupported sidebar module", module)
		}
		items := module["items"].([]any)
		item := objectMap(objectMap(objectMap(items[0])["item"])["content"])
		switch item["__typename"] {
		case "TimelineUser":
			if objectMap(objectMap(item["user_results"])["result"])["rest_id"] != "42" || item["user_display_type"] != "UserCompact" {
				t.Fatal("recommendation lost user or display type", item)
			}
		case "TimelineTrend":
			link, e := url.Parse(objectMap(item["url"])["url"].(string))
			if e != nil || link.Scheme != "twitter" || link.Host != "search" || link.Query().Get("query") != "#Тест" {
				t.Fatal("trend search link is invalid", item)
			}
		default:
			t.Fatal("unexpected sidebar item", item)
		}
	}
}
