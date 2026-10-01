package main

import (
	"flashlarry-api/internal/social"
	"net/http"
	"strconv"
	"strings"
)

func (g *Gateway) homeFeed(r *http.Request, vars map[string]any) ([]map[string]any, error) {
	if g.socialURL == "" {
		posts, e := g.timeline(r.Context(), count(vars))
		if e != nil {
			return nil, e
		}
		return g.instructions(r.Context(), posts)
	}
	name := r.PathValue("operation")
	following := strings.Contains(name, "Latest") || name == "HomeTimelineLatest"
	var feed social.FeedResult
	if e := g.call(r.Context(), "POST", g.socialURL+"/feed", social.FeedRequest{UserID: viewerID(r.Context()), Count: count(vars), Cursor: social.Str(vars, "cursor"), Following: following}, &feed); e != nil {
		return nil, e
	}
	posts := []Tweet{}
	for _, id := range feed.TweetIDs {
		var post Tweet
		if e := g.call(r.Context(), "GET", g.tweetsURL+"/tweets/"+id, nil, &post); e != nil {
			return nil, e
		}
		posts = append(posts, post)
	}
	instructions, e := g.homeInstructions(r.Context(), posts)
	if e != nil {
		return nil, e
	}
	entries := instructions[0]["entries"].([]map[string]any)
	postIDs := map[string]bool{}
	for _, id := range feed.TweetIDs {
		postIDs["tweet-"+id] = true
	}
	ordinal := feed.Offset
	for _, entry := range entries {
		rank := feed.SortBase - int64(ordinal)*10
		if postIDs[social.Str(entry, "entry_id", "entryId")] {
			ordinal++
		} else {
			rank += 5
		}
		index := strconv.FormatInt(rank, 10)
		entry["sort_index"] = index
		entry["sortIndex"] = index
	}
	if feed.Cursor != "" {
		content := map[string]any{"__typename": "TimelineTimelineCursor", "entryType": "TimelineTimelineCursor", "cursor_type": "Bottom", "cursorType": "Bottom", "value": feed.Cursor}
		entries = append(entries, map[string]any{"entry_id": "cursor-bottom", "entryId": "cursor-bottom", "sort_index": "0", "sortIndex": "0", "content": content})
	}
	instructions[0]["entries"] = entries
	return instructions, nil
}
