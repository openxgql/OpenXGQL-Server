package social

import (
	"context"
	"database/sql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"flashlarry-api/internal/siteconfig"
)

func TestIntegration(t *testing.T) {
	dsn := os.Getenv("FLASHLARRY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("isolated database required")
	}
	if !strings.Contains(dsn, "flashlarry_integration") {
		t.Fatal("refusing non-test database")
	}
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(12)
	ctx := context.Background()
	s := &Store{DB: db}
	if e = s.Init(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(`TRUNCATE users,tweets,fl_entities,fl_relations,fl_preferences,fl_reports,fl_audit,fl_notifications,fl_ad_events,fl_ad_campaigns,fl_jobs RESTART IDENTITY CASCADE`); e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for _, name := range []string{"flashlarry", "alice", "bob", "carol"} {
		var id string
		if e = db.QueryRow(`INSERT INTO users(screen_name,name) VALUES($1,$1) RETURNING id::text`, name).Scan(&id); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, id)
	}
	alice, bob, carol := ids[1], ids[2], ids[3]
	post := func(owner, text string) string {
		var id string
		if e = db.QueryRow(`INSERT INTO tweets(author_id,text) VALUES($1,$2) RETURNING id::text`, owner, text).Scan(&id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	tweet := post(alice, "Hello #FlashLarry")
	other := post(bob, "Bob post")
	call := func(name, user string, v map[string]any) Result {
		t.Helper()
		name = mapName(name)
		var spec Spec
		found := false
		for _, x := range Registry {
			if (x.Name == name || x.Action == name) && x.Handler != "excluded" {
				spec = x
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing spec %s", name)
		}
		r, e := s.Execute(ctx, spec, user, v)
		if e != nil {
			t.Fatalf("%s: %v", name, e)
		}
		return r
	}
	rejected := func(name, user string, v map[string]any, status int) {
		t.Helper()
		name = mapName(name)
		var spec Spec
		for _, x := range Registry {
			if x.Name == name || x.Action == name {
				spec = x
				break
			}
		}
		_, e := s.Execute(ctx, spec, user, v)
		if e == nil || ErrorStatus(e) != status {
			t.Fatalf("%s: wanted %d got %v", name, status, e)
		}
	}
	t.Run("bookmarks ownership", func(t *testing.T) {
		call("CreateBookmark", bob, map[string]any{"tweet_id": tweet})
		r := call("Bookmarks", bob, nil)
		if len(r.TweetIDs) != 1 || r.TweetIDs[0] != tweet {
			t.Fatal(r)
		}
		folder := call("createBookmarkFolder", bob, map[string]any{"name": "Saved"})
		id := Str(folder.Value, "id")
		call("bookmarkTweetToFolder", bob, map[string]any{"tweet_id": tweet, "folder_id": id})
		rejected("EditBookmarkFolder", alice, map[string]any{"folder_id": id, "name": "stolen"}, 404)
		r = call("BookmarkFolderTimeline", bob, map[string]any{"folder_id": id})
		if len(r.TweetIDs) != 1 {
			t.Fatal(r)
		}
		call("DeleteBookmark", bob, map[string]any{"tweet_id": tweet})
		r = call("Bookmarks", bob, nil)
		if len(r.TweetIDs) != 0 {
			t.Fatal(r)
		}
	})
	t.Run("private follow and block", func(t *testing.T) {
		if _, e = db.Exec(`UPDATE users SET protected=true WHERE id=$1`, alice); e != nil {
			t.Fatal(e)
		}
		allowed, e := s.VisiblePosts(ctx, bob, []string{tweet})
		if e != nil || len(allowed) > 0 {
			t.Fatal(allowed, e)
		}
		call("FollowUser", bob, map[string]any{"user_id": alice})
		var n int
		db.QueryRow(`SELECT count(*) FROM follows WHERE follower_id=$1 AND target_id=$2`, bob, alice).Scan(&n)
		if n != 0 {
			t.Fatal("private follow bypass")
		}
		call("AcceptFollowRequest", alice, map[string]any{"user_id": bob})
		allowed, e = s.VisiblePosts(ctx, bob, []string{tweet})
		if e != nil || len(allowed) != 1 {
			t.Fatal(allowed, e)
		}
		call("BlockUser", alice, map[string]any{"user_id": bob})
		allowed, e = s.VisiblePosts(ctx, bob, []string{tweet})
		if e != nil || len(allowed) > 0 {
			t.Fatal(allowed, e)
		}
		rejected("FollowUser", bob, map[string]any{"user_id": alice}, 403)
		call("UnblockUser", alice, map[string]any{"user_id": bob})
		db.Exec(`UPDATE users SET protected=false WHERE id=$1`, alice)
	})
	t.Run("pin and reply ownership", func(t *testing.T) {
		rejected("PinTweet", bob, map[string]any{"tweet_id": tweet}, 403)
		call("PinTweet", alice, map[string]any{"tweet_id": tweet})
		state, e := s.UserState(ctx, alice, bob)
		if e != nil || len(state["pinned"].([]string)) != 1 {
			t.Fatal(state, e)
		}
		var reply string
		db.QueryRow(`INSERT INTO tweets(author_id,text,reply_to_id) VALUES($1,'reply',$2) RETURNING id::text`, bob, tweet).Scan(&reply)
		rejected("ModerateTweet", carol, map[string]any{"tweet_id": reply}, 403)
		call("ModerateTweet", alice, map[string]any{"tweet_id": reply})
		call("UnmoderateTweet", alice, map[string]any{"tweet_id": reply})
	})
	t.Run("lists and communities", func(t *testing.T) {
		list := call("CreateList", alice, map[string]any{"name": "Friends", "is_private": true})
		id := Str(list.Value, "id")
		rejected("ListByRestId", bob, map[string]any{"list_id": id}, 404)
		call("ListAddMember", alice, map[string]any{"list_id": id, "user_id": bob})
		r := call("ListLatestTweetsTimeline", alice, map[string]any{"list_id": id})
		if !contains(r.TweetIDs, other) || contains(r.TweetIDs, tweet) {
			t.Fatal(r)
		}
		community := call("CreateCommunity", alice, map[string]any{"name": "Test"})
		comm := Str(community.Value, "id")
		call("JoinCommunity", bob, map[string]any{"community_id": comm})
		rejected("CommunityUpdateRole", bob, map[string]any{"community_id": comm, "user_id": carol}, 404)
		call("CommunityUpdateRole", alice, map[string]any{"community_id": comm, "user_id": bob, "role": "Moderator"})
		if e = s.ValidatePost(ctx, bob, map[string]any{"community_id": comm}); e != nil {
			t.Fatal(e)
		}
		if e = s.CommitPost(ctx, bob, other, map[string]any{"community_id": comm}); e != nil {
			t.Fatal(e)
		}
		r = call("CommunityTweetsTimeline", alice, map[string]any{"community_id": comm})
		if len(r.TweetIDs) != 1 {
			t.Fatal(r)
		}
	})
	t.Run("articles and notes", func(t *testing.T) {
		article := call("ArticleEntityDraftCreate", alice, map[string]any{"title": "Article", "content": "Body"})
		id := Str(article.Value, "id")
		rejected("ArticleEntityPublish", bob, map[string]any{"article_entity_id": id}, 404)
		published := call("ArticleEntityPublish", alice, map[string]any{"article_entity_id": id})
		if Str(published.Value, "tweet_id") == "" {
			t.Fatal(published)
		}
		call("ArticleEntityResultByRestId", bob, map[string]any{"article_entity_id": id})
		call("ArticleEntityUnpublish", alice, map[string]any{"article_entity_id": id})
		rejected("ArticleEntityResultByRestId", bob, map[string]any{"article_entity_id": id}, 404)
		note := call("BirdwatchCreateNote", bob, map[string]any{"tweet_id": tweet, "summary": "Context"})
		noteID := Str(note.Value, "id")
		for _, u := range []string{alice, bob, carol} {
			call("BirdwatchCreateRating", u, map[string]any{"note_id": noteID, "helpful": true})
		}
		r := call("BirdwatchFetchOneNote", alice, map[string]any{"note_id": noteID})
		if r.Value["status"] != "HELPFUL" {
			t.Fatal(r)
		}
		rejected("BirdwatchDeleteNote", alice, map[string]any{"note_id": noteID}, 404)
	})
	t.Run("moderation and audit", func(t *testing.T) {
		r, e := s.Report(ctx, bob, map[string]any{"target_kind": "tweet", "target_id": tweet, "reason": "spam"})
		if e != nil {
			t.Fatal(e)
		}
		id := Str(r, "id")
		if e = s.ReviewReport(ctx, id, "hide_tweet", "test", "admin"); e != nil {
			t.Fatal(e)
		}
		allowed, e := s.VisiblePosts(ctx, alice, []string{tweet})
		if e != nil || len(allowed) > 0 {
			t.Fatal(allowed, e)
		}
		if e = s.ReviewReport(ctx, id, "restore_tweet", "test", "admin"); e != nil {
			t.Fatal(e)
		}
		if e = s.SetVerified(ctx, bob, true, "admin"); e != nil {
			t.Fatal(e)
		}
		var count int
		if e = db.QueryRow(`SELECT count(*) FROM fl_audit WHERE actor='admin'`).Scan(&count); e != nil || count < 3 {
			t.Fatal(count, e)
		}
	})
	t.Run("ad cap and click deduplication", func(t *testing.T) {
		ad, e := s.CreateAd(ctx, map[string]any{"name": "Test ad", "text": "Ad body", "screen_name": "flashlarry", "target_url": "https://example.com/", "active": true, "impression_limit": float64(3)}, "admin")
		if e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		var mu sync.Mutex
		deliveries := []string{}
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a, e := s.NextAd(ctx, bob)
				if e != nil {
					t.Error(e)
				}
				if a != nil {
					mu.Lock()
					deliveries = append(deliveries, a.DeliveryID)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		for len(deliveries) < 3 {
			a, e := s.NextAd(ctx, bob)
			if e != nil {
				t.Fatal(e)
			}
			if a == nil {
				break
			}
			deliveries = append(deliveries, a.DeliveryID)
		}
		if len(deliveries) != 3 {
			t.Fatal(deliveries)
		}
		for i := 0; i < 2; i++ {
			if _, e = s.AdClick(ctx, deliveries[0]); e != nil {
				t.Fatal(e)
			}
		}
		var clicks int
		db.QueryRow(`SELECT count(*) FROM fl_ad_events WHERE campaign_id=$1 AND kind='click'`, Str(ad, "id")).Scan(&clicks)
		if clicks != 1 {
			t.Fatal(clicks)
		}
	})
	t.Run("scheduled exactly once", func(t *testing.T) {
		r := call("CreateScheduledTweet", alice, map[string]any{"tweet_text": "Scheduled", "execute_at": strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)})
		id := Str(r.Value, "id")
		if _, e = db.Exec(`UPDATE fl_jobs SET due_at=now()-interval '1 minute' WHERE id::text=$1`, id); e != nil {
			t.Fatal(e)
		}
		var wg sync.WaitGroup
		for i := 0; i < 3; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if e := s.PublishScheduled(ctx); e != nil {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
		var n int
		db.QueryRow(`SELECT count(*) FROM tweets WHERE text='Scheduled'`).Scan(&n)
		if n != 1 {
			t.Fatal(n)
		}
	})
	t.Run("notifications and discovery", func(t *testing.T) {
		call("FollowUser", bob, map[string]any{"user_id": alice})
		r := call("NotificationsTimeline", alice, nil)
		if len(r.Value) == 0 {
			t.Fatal(r)
		}
		r = call("ExplorePage", alice, nil)
		if len(r.TweetIDs) == 0 {
			t.Fatal(r)
		}
		r = call("SearchTimeline", bob, map[string]any{"raw_query": "Hello"})
		if len(r.TweetIDs) != 1 {
			t.Fatal(r)
		}
	})
	t.Run("runtime feed priority pagination and suspension", func(t *testing.T) {
		freshUsers := []string{}
		for _, name := range []string{"feedviewer", "priorityauthor", "newauthor", "privateauthor", "hiddenauthor"} {
			var id string
			if e := db.QueryRow(`INSERT INTO users(screen_name,name) VALUES($1,$1) RETURNING id::text`, name).Scan(&id); e != nil {
				t.Fatal(e)
			}
			freshUsers = append(freshUsers, id)
		}
		viewer, priorityAuthor, newAuthor := freshUsers[0], freshUsers[1], freshUsers[2]
		older := post(priorityAuthor, "Priority example")
		newer := post(newAuthor, "Fresh example")
		private := post(freshUsers[3], "Private example")
		hidden := post(freshUsers[4], "Hidden example")
		if _, e := db.Exec(`UPDATE users SET protected=true WHERE id=$1`, freshUsers[3]); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec(`UPDATE tweets SET hidden=true WHERE id=$1`, hidden); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec(`UPDATE tweets SET created_at=now()-interval '1 hour' WHERE id=$1`, older); e != nil {
			t.Fatal(e)
		}
		for _, id := range []string{priorityAuthor, freshUsers[3], freshUsers[4]} {
			if e := s.SetPriority(ctx, id, 100, "integration"); e != nil {
				t.Fatal(e)
			}
		}
		if e := s.SetPriority(ctx, newAuthor, -100, "integration"); e != nil {
			t.Fatal(e)
		}
		cfg, e := siteconfig.Load(ctx, db)
		if e != nil {
			t.Fatal(e)
		}
		cfg.FeedMode, cfg.AuthMode = "latest", "telegram"
		if _, e = siteconfig.Save(ctx, db, cfg, "integration"); e != nil {
			t.Fatal(e)
		}
		latest, e := s.Feed(ctx, FeedRequest{UserID: viewer, Count: 100})
		if e != nil {
			t.Fatal(e)
		}
		if len(latest.TweetIDs) < 2 || latest.TweetIDs[0] != newer || latest.TweetIDs[1] != older || contains(latest.TweetIDs, private) || contains(latest.TweetIDs, hidden) {
			t.Fatal(latest)
		}
		cfg, _ = siteconfig.Load(ctx, db)
		cfg.FeedMode = "algorithm"
		if _, e = siteconfig.Save(ctx, db, cfg, "integration"); e != nil {
			t.Fatal(e)
		}
		first, e := s.Feed(ctx, FeedRequest{UserID: viewer, Count: 1})
		if e != nil {
			t.Fatal(e)
		}
		if len(first.TweetIDs) != 1 || first.TweetIDs[0] != older || first.Cursor == "" {
			t.Fatal(first)
		}
		if _, e := s.Feed(ctx, FeedRequest{UserID: newAuthor, Count: 1, Cursor: first.Cursor}); ErrorStatus(e) != 400 {
			t.Fatal("foreign cursor accepted", e)
		}
		all := append([]string{}, first.TweetIDs...)
		cursor := first.Cursor
		for cursor != "" {
			next, e := s.Feed(ctx, FeedRequest{UserID: viewer, Count: 1, Cursor: cursor})
			if e != nil {
				t.Fatal(e)
			}
			if next.SortBase != first.SortBase {
				t.Fatal("rank snapshot changed")
			}
			for _, id := range next.TweetIDs {
				if contains(all, id) {
					t.Fatal("duplicate page", id)
				}
				all = append(all, id)
			}
			cursor = next.Cursor
		}
		if !contains(all, newer) || contains(all, private) || contains(all, hidden) {
			t.Fatal(all)
		}
		if e := s.SetSuspended(ctx, priorityAuthor, true, "integration"); e != nil {
			t.Fatal(e)
		}
		feed, e := s.Feed(ctx, FeedRequest{UserID: viewer, Count: 100})
		if e != nil || contains(feed.TweetIDs, older) {
			t.Fatal("suspended author visible", feed, e)
		}
		if _, e := s.Feed(ctx, FeedRequest{UserID: priorityAuthor, Count: 1}); ErrorStatus(e) != 403 {
			t.Fatal("suspended viewer allowed", e)
		}
		if e := s.SetSuspended(ctx, priorityAuthor, false, "integration"); e != nil {
			t.Fatal(e)
		}
		visible, e := s.VisiblePosts(ctx, viewer, []string{older})
		if e != nil || len(visible) != 1 {
			t.Fatal("restore failed", visible, e)
		}
		if e := s.SetPremium(ctx, priorityAuthor, true, "integration"); e != nil {
			t.Fatal(e)
		}
		var premium, verified bool
		if e := db.QueryRow(`SELECT premium,verified FROM users WHERE id=$1`, priorityAuthor).Scan(&premium, &verified); e != nil || !premium || verified {
			t.Fatal("manual premium unexpectedly changes badge", premium, verified, e)
		}
		cfg, _ = siteconfig.Load(ctx, db)
		cfg.FeedMode = "latest"
		if _, e = siteconfig.Save(ctx, db, cfg, "integration"); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Feed(ctx, FeedRequest{UserID: viewer, Count: 1, Cursor: first.Cursor}); ErrorStatus(e) != 400 {
			t.Fatal("cursor survived config change", e)
		}
	})
}

func mapName(n string) string {
	m := map[string]string{"BlockUser": "blockUserMutation", "UnblockUser": "unblockUserMutation", "PinTweet": "pinTweetMutation"}
	if x := m[n]; x != "" {
		return x
	}
	return n
}
