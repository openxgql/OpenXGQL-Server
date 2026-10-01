package main

import (
	"flashlarry-api/internal/social"
	"testing"
)

func TestRelayTimelineResponse(t *testing.T) {
	s, ok := social.Lookup("bookmarksQuery", "AIBqFW-PUyUrQycHnEB3-Q")
	if !ok {
		t.Fatal("schema absent")
	}
	expected := []map[string]any{{"__typename": "TimelineAddEntries", "entries": []any{map[string]any{"entry_id": "tweet-1"}}}}
	v := socialView{value: map[string]any{}, timeline: map[string]any{"id": "bookmarks-1", "timeline": map[string]any{"instructions": expected}}}
	data := v.project(s.Selections, map[string]any{}, true)
	timeline := objectMap(objectMap(data["bookmark_timeline_v2"])["timeline"])
	if timeline["instructions"] == nil {
		t.Fatal(data)
	}
}
func TestRelayOwnerIsResolvedValue(t *testing.T) {
	s, ok := social.Lookup("listDetailQuery", "Ff-4sSVkJj8TRV-1C2a21w")
	if !ok {
		t.Fatal("schema absent")
	}
	user := map[string]any{"__typename": "User", "rest_id": "42", "core": map[string]any{"screen_name": "alice", "name": "Alice"}}
	v := socialView{value: map[string]any{"__typename": "List", "rest_id": "5", "name": "Friends", "mode": "Private", "owner_results": map[string]any{"result": user}}}
	data := v.project(s.Selections, map[string]any{}, true)
	list := objectMap(data["list_by_rest_id_v2"])
	owner := objectMap(objectMap(list["owner_results"])["result"])
	if owner["rest_id"] != "42" || list["mode"] != "Private" {
		t.Fatal(data)
	}
}
func TestUnknownGraphQLIDIsRejected(t *testing.T) {
	if _, ok := social.Lookup("CreateBookmark", "invented-id"); ok {
		t.Fatal("invented query ID accepted")
	}
}
func TestExcludedPaymentRegistry(t *testing.T) {
	found := 0
	for _, s := range social.Registry {
		if s.Action == "payments" {
			found++
			if s.Handler != "excluded" {
				t.Fatal(s)
			}
		}
	}
	if found == 0 {
		t.Fatal("payments not excluded")
	}
}
