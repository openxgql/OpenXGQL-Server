package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/siteconfig"
	"flashlarry-api/internal/xalgorithm"
)

const feedSchema = `CREATE TABLE IF NOT EXISTS fl_feed_sessions(id TEXT PRIMARY KEY,user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,mode TEXT NOT NULL,revision BIGINT NOT NULL,following BOOLEAN NOT NULL,post_ids JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now(),expires_at TIMESTAMPTZ NOT NULL DEFAULT now()+interval '15 minutes');
CREATE INDEX IF NOT EXISTS fl_feed_sessions_expiry ON fl_feed_sessions(expires_at);
CREATE TABLE IF NOT EXISTS fl_feed_deliveries(user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,tweet_id BIGINT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE,deliveries INTEGER NOT NULL DEFAULT 1,last_served_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(user_id,tweet_id));
CREATE INDEX IF NOT EXISTS fl_feed_deliveries_post ON fl_feed_deliveries(tweet_id);`

type FeedRequest struct {
	UserID    string `json:"user_id"`
	Count     int    `json:"count"`
	Cursor    string `json:"cursor"`
	Following bool   `json:"following"`
}
type FeedResult struct {
	TweetIDs []string `json:"tweet_ids"`
	Cursor   string   `json:"cursor"`
	Mode     string   `json:"mode"`
	SortBase int64    `json:"sort_base"`
	Offset   int      `json:"offset"`
}

func (s *Store) Feed(ctx context.Context, r FeedRequest) (FeedResult, error) {
	out := FeedResult{TweetIDs: []string{}}
	if !validID(r.UserID) {
		return out, fail(401, "viewer required")
	}
	var allowed bool
	if e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::bigint AND NOT suspended)`, r.UserID).Scan(&allowed); e != nil {
		return out, e
	}
	if !allowed {
		return out, fail(403, "account is suspended or missing")
	}
	r.Count = limit(map[string]any{"count": float64(r.Count)})
	cfg, e := siteconfig.Load(ctx, s.DB)
	if e != nil {
		return out, e
	}
	out.Mode = cfg.FeedMode
	if r.Following {
		out.Mode = "latest"
	}
	var ids []string
	var session string
	var offset int
	var created time.Time
	if r.Cursor != "" {
		parts := strings.Split(r.Cursor, ":")
		if len(parts) != 3 || parts[0] != "fl" || len(parts[1]) != 48 {
			return out, fail(400, "invalid feed cursor")
		}
		session = parts[1]
		offset, e = strconv.Atoi(parts[2])
		if e != nil || offset < 0 {
			return out, fail(400, "invalid feed cursor")
		}
		var raw []byte
		e = s.DB.QueryRowContext(ctx, `SELECT post_ids,created_at FROM fl_feed_sessions WHERE id=$1 AND user_id=$2::bigint AND revision=$3 AND mode=$4 AND following=$5 AND expires_at>now()`, session, r.UserID, cfg.Revision, out.Mode, r.Following).Scan(&raw, &created)
		if e == sql.ErrNoRows {
			return out, fail(400, "feed cursor expired; refresh the feed")
		}
		if e != nil {
			return out, e
		}
		if e = json.Unmarshal(raw, &ids); e != nil {
			return out, e
		}
	} else {
		ids, e = s.feedCandidates(ctx, r.UserID, out.Mode, r.Following)
		if e != nil {
			return out, e
		}
		session = randomID()
		created = time.Now().UTC()
		if _, e = s.DB.ExecContext(ctx, `INSERT INTO fl_feed_sessions(id,user_id,mode,revision,following,post_ids,created_at) VALUES($1,$2::bigint,$3,$4,$5,$6,$7)`, session, r.UserID, out.Mode, cfg.Revision, r.Following, Encode(ids), created); e != nil {
			return out, e
		}
		// Bounded retention; serving history is an aggregate, not an append-only log.
		s.DB.ExecContext(ctx, `DELETE FROM fl_feed_sessions WHERE expires_at<now()`)
	}
	if offset > len(ids) {
		return out, fail(400, "invalid feed offset")
	}
	end := min(offset+r.Count, len(ids))
	out.TweetIDs, e = s.VisiblePosts(ctx, r.UserID, ids[offset:end])
	if e != nil {
		return out, e
	}
	if end < len(ids) {
		out.Cursor = "fl:" + session + ":" + strconv.Itoa(end)
	}
	out.SortBase = created.UnixMilli() * 1000
	out.Offset = offset
	if len(out.TweetIDs) > 0 {
		_, e = s.DB.ExecContext(ctx, `INSERT INTO fl_feed_deliveries(user_id,tweet_id) SELECT $1::bigint,x::bigint FROM unnest($2::text[]) x ON CONFLICT(user_id,tweet_id) DO UPDATE SET deliveries=fl_feed_deliveries.deliveries+1,last_served_at=now()`, r.UserID, out.TweetIDs)
	}
	return out, e
}

type feedPost struct {
	id, author, text, parent                         string
	created                                          time.Time
	likes, reposts, replies, views, priority, served int
	following, mutual                                bool
}

var interestWords = regexp.MustCompile(`[\p{L}\p{N}_#]{3,}`)

func probability(value float64) float64 { return math.Max(0, math.Min(0.95, value)) }
func (s *Store) feedCandidates(ctx context.Context, user, mode string, following bool) ([]string, error) {
	filter := ""
	if following {
		filter = ` AND (t.author_id=$1::bigint OR EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1::bigint AND f.target_id=t.author_id))`
	}
	if mode == "algorithm" {
		filter += ` AND t.created_at>now()-interval '48 hours'`
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text,t.author_id::text,t.text,COALESCE(t.reply_to_id::text,''),t.created_at,
 (SELECT count(*) FROM reactions l WHERE l.tweet_id=t.id),(SELECT count(*) FROM retweets r WHERE r.tweet_id=t.id),(SELECT count(*) FROM tweets reply WHERE reply.reply_to_id=t.id AND NOT reply.hidden),
 (SELECT COALESCE(sum(d.deliveries),0) FROM fl_feed_deliveries d WHERE d.tweet_id=t.id),COALESCE(p.priority,0),COALESCE(d.deliveries,0),
 EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=$1::bigint AND f.target_id=t.author_id),EXISTS(SELECT 1 FROM follows f WHERE f.follower_id=t.author_id AND f.target_id=$1::bigint)
 FROM tweets t JOIN users u ON u.id=t.author_id LEFT JOIN fl_account_priorities p ON p.user_id=t.author_id LEFT JOIN fl_feed_deliveries d ON d.user_id=$1::bigint AND d.tweet_id=t.id WHERE `+visibility+filter+` ORDER BY t.id DESC LIMIT 500`, user)
	if e != nil {
		return nil, e
	}
	posts := []feedPost{}
	for rows.Next() {
		var p feedPost
		if e = rows.Scan(&p.id, &p.author, &p.text, &p.parent, &p.created, &p.likes, &p.reposts, &p.replies, &p.views, &p.priority, &p.served, &p.following, &p.mutual); e != nil {
			rows.Close()
			return nil, e
		}
		posts = append(posts, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	if mode == "latest" {
		ids := []string{}
		for _, p := range posts {
			ids = append(ids, p.id)
		}
		return ids, nil
	}
	// Local probability estimator replaces Phoenix inference. Published value-model
	// arithmetic consumes these probabilities, never raw engagement counts.
	history, e := s.DB.QueryContext(ctx, `SELECT t.author_id::text,t.text FROM tweets t JOIN fl_feed_deliveries d ON d.tweet_id=t.id AND d.user_id=$1::bigint WHERE EXISTS(SELECT 1 FROM reactions r WHERE r.tweet_id=t.id AND r.user_id=$1::bigint) OR EXISTS(SELECT 1 FROM retweets r WHERE r.tweet_id=t.id AND r.user_id=$1::bigint) ORDER BY t.id DESC LIMIT 200`, user)
	if e != nil {
		return nil, e
	}
	authors, words := map[string]int{}, map[string]int{}
	for history.Next() {
		var author, text string
		if e = history.Scan(&author, &text); e != nil {
			history.Close()
			return nil, e
		}
		authors[author]++
		for _, w := range interestWords.FindAllString(strings.ToLower(text), -1) {
			words[w]++
		}
	}
	e = history.Err()
	history.Close()
	if e != nil {
		return nil, e
	}
	candidates := []xalgorithm.Candidate{}
	parent := map[string]string{}
	for _, p := range posts {
		parent[p.id] = p.parent
	}
	for _, p := range posts {
		tokens := interestWords.FindAllString(strings.ToLower(p.text), -1)
		affinity := 0.0
		for _, word := range tokens {
			if words[word] > 0 {
				affinity++
			}
		}
		affinity /= float64(max(1, len(tokens)))
		authorAffinity := float64(authors[p.author]) / float64(authors[p.author]+10)
		network := p.following || p.author == user
		networkPrior := 0.0
		if network {
			networkPrior = 0.04
		}
		heads := map[string]float64{"favorite": probability((float64(p.likes)+0.5)/float64(p.views+20) + authorAffinity*0.12 + affinity*0.08 + networkPrior), "reply": probability((float64(p.replies)+0.1)/float64(p.views+40) + authorAffinity*0.02 + affinity*0.02), "retweet": probability((float64(p.reposts)+0.2)/float64(p.views+30) + affinity*0.02), "dwell": probability(0.2 + affinity*0.4), "post_unexplored": 1 / float64(1+p.served)}
		root := p.id
		seen := map[string]bool{}
		for next := p.parent; next != "" && !seen[next]; next = parent[next] {
			root = next
			seen[next] = true
		}
		candidates = append(candidates, xalgorithm.Candidate{ID: p.id, AuthorID: p.author, ConversationID: root, Heads: heads, InNetwork: &network, MutualFollow: p.following && p.mutual, Reply: p.parent != ""})
	}
	w := xalgorithm.DefaultWeights()
	w.AuthorDiversity = true
	w.DiversityDecay = 0.75
	w.DiversityFloor = 0.6
	_, scores := xalgorithm.ValueScores(w, 0.85, candidates)
	now := time.Now()
	for i, p := range posts {
		freshness := math.Exp(-math.Max(0, now.Sub(p.created).Hours()) / 24)
		priority := math.Exp2(float64(p.priority) / 25)
		scores[i] *= freshness * priority / (1 + 0.1*float64(p.served))
	}
	selected := xalgorithm.TopK(candidates, scores, len(candidates))
	ids := []string{}
	for _, i := range selected {
		ids = append(ids, posts[i].id)
	}
	return ids, nil
}
