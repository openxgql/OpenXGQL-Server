package social

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"strconv"
	"time"

	"flashlarry-api/internal/common"
)

func randomID() string {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}

type Ad struct {
	ID         string `json:"id"`
	TweetID    string `json:"tweet_id"`
	DeliveryID string `json:"delivery_id"`
	TargetURL  string `json:"target_url"`
}

func (s *Store) NextAd(ctx context.Context, user string) (*Ad, error) {
	if !validID(user) {
		return nil, fail(401, "viewer required")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	a := &Ad{}
	e = tx.QueryRowContext(ctx, `SELECT a.id::text,a.tweet_id::text,a.target_url FROM fl_ad_campaigns a JOIN tweets t ON t.id=a.tweet_id JOIN users u ON u.id=t.author_id WHERE a.active AND a.starts_at<=now() AND (a.ends_at IS NULL OR a.ends_at>now()) AND NOT t.hidden AND NOT u.suspended AND NOT u.protected AND (a.impression_limit=0 OR (SELECT count(*) FROM fl_ad_events e WHERE e.campaign_id=a.id AND e.kind='impression')<a.impression_limit) AND NOT EXISTS(SELECT 1 FROM fl_relations b WHERE b.owner_id=$1::bigint AND b.kind IN ('mute','block') AND b.target_id=t.author_id::text OR b.owner_id=t.author_id AND b.kind='block' AND b.target_id=$1::text) ORDER BY (SELECT count(*) FROM fl_ad_events e WHERE e.campaign_id=a.id AND e.viewer_id=$1 AND e.kind='impression'),a.id LIMIT 1 FOR UPDATE OF a SKIP LOCKED`, user).Scan(&a.ID, &a.TweetID, &a.TargetURL)
	if e == sql.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	// Recheck with a fresh statement snapshot after obtaining the campaign lock.
	var capacity bool
	if e = tx.QueryRowContext(ctx, `SELECT impression_limit=0 OR (SELECT count(*) FROM fl_ad_events WHERE campaign_id=$1 AND kind='impression')<impression_limit FROM fl_ad_campaigns WHERE id=$1::bigint`, a.ID).Scan(&capacity); e != nil {
		return nil, e
	}
	if !capacity {
		return nil, nil
	}
	a.DeliveryID = randomID()
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_ad_events(campaign_id,viewer_id,delivery_id,kind) VALUES($1,$2,$3,'impression')`, a.ID, user, a.DeliveryID); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return a, nil
}
func (s *Store) AdClick(ctx context.Context, delivery string) (string, error) {
	if len(delivery) != 48 {
		return "", fail(404, "ad delivery not found")
	}
	var target string
	var campaign, viewer int64
	e := s.DB.QueryRowContext(ctx, `SELECT a.target_url,a.id,e.viewer_id FROM fl_ad_events e JOIN fl_ad_campaigns a ON a.id=e.campaign_id WHERE e.delivery_id=$1 AND e.kind='impression' AND e.created_at>now()-interval '7 days'`, delivery).Scan(&target, &campaign, &viewer)
	if e == sql.ErrNoRows {
		return "", fail(404, "ad delivery expired")
	}
	if e != nil {
		return "", e
	}
	_, e = s.DB.ExecContext(ctx, `INSERT INTO fl_ad_events(campaign_id,viewer_id,delivery_id,kind) VALUES($1,$2,$3,'click') ON CONFLICT(delivery_id,kind) DO NOTHING`, campaign, viewer, delivery)
	return target, e
}
func (s *Store) adOperation(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	a := spec.Action
	switch a {
	case "AdAccounts":
		state, e := s.profileState(ctx, user)
		return Result{Value: map[string]any{"accounts": []any{map[string]any{"id": user, "name": state["name"], "status": "LOCAL", "currency": ""}}, "items": []any{}}}, e
	case "AudienceEstimate", "BoostAudienceEstimate":
		var n int
		e := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE NOT suspended`).Scan(&n)
		return Result{Value: map[string]any{"estimate": n, "audience_size": n, "minimum": n, "maximum": n}}, e
	case "QuickPromoteEligibility":
		return Result{Value: map[string]any{"eligible": true, "requires_admin_approval": true}}, nil
	case "QuickPromoteBoostContentCheck":
		id, e := require(v, "tweetId", "tweet_id", "restId")
		if e != nil {
			return Result{}, e
		}
		var owner string
		var hidden bool
		e = s.DB.QueryRowContext(ctx, `SELECT author_id::text,hidden FROM tweets WHERE id::text=$1`, id).Scan(&owner, &hidden)
		if e == sql.ErrNoRows {
			return Result{}, fail(404, "post not found")
		}
		return Result{Value: map[string]any{"eligible": owner == user && !hidden, "tweet_id": id}}, e
	case "CreateQuickPromotion":
		tweet, e := require(v, "tweetId", "tweet_id", "restId")
		if e != nil {
			return Result{}, e
		}
		var owner string
		e = s.DB.QueryRowContext(ctx, `SELECT author_id::text FROM tweets WHERE id::text=$1 AND NOT hidden`, tweet).Scan(&owner)
		if e == sql.ErrNoRows {
			return Result{}, fail(404, "post not found")
		}
		if e != nil {
			return Result{}, e
		}
		if owner != user {
			return Result{}, fail(403, "only own posts can be promoted")
		}
		var id int64
		target := Str(v, "target_url", "url")
		if target == "" {
			var handle string
			_ = s.DB.QueryRowContext(ctx, `SELECT screen_name FROM users WHERE id=$1::bigint`, user).Scan(&handle)
			target = common.PublicBaseURL() + "/" + handle + "/status/" + tweet
		}
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return Result{}, fail(400, "valid HTTP(S) target URL required")
		}
		e = s.DB.QueryRowContext(ctx, `INSERT INTO fl_ad_campaigns(name,tweet_id,target_url,active) VALUES($1,$2,$3,false) RETURNING id`, "Promotion by "+user, tweet, target).Scan(&id)
		return Result{Value: map[string]any{"id": strconv.FormatInt(id, 10), "status": "pending_approval", "active": false}}, e
	case "UserPromotableTweets":
		return s.timeline(ctx, Spec{Handler: "timeline", Action: "UserTweets"}, user, v)
	case "UserPromotedTweets":
		rows, e := s.DB.QueryContext(ctx, `SELECT DISTINCT t.id::text FROM fl_ad_campaigns a JOIN tweets t ON t.id=a.tweet_id WHERE t.author_id=$1::bigint ORDER BY t.id::text DESC LIMIT $2`, user, limit(v))
		ids, e := scanIDs(rows, e)
		return Result{TweetIDs: ids, Timeline: true, UserID: user}, e
	case "Coupons", "EnrollCoupon":
		return Result{Value: map[string]any{"items": []any{}, "coupons": []any{}, "eligible": false}}, nil
	case "Budgets", "BudgetsByCurrency":
		return Result{Value: map[string]any{"budgets": []any{}, "items": []any{}, "currency": "", "billing_enabled": false}}, nil
	case "TargetingCatalogSearch":
		items, e := s.listEntities(ctx, user, "topic", "", Str(v, "query"), true)
		return Result{Value: map[string]any{"items": items}}, e
	}
	ads, e := s.AdminAds(ctx)
	if e != nil {
		return Result{}, e
	}
	items := []map[string]any{}
	for _, ad := range ads {
		var owner string
		if s.DB.QueryRowContext(ctx, `SELECT author_id::text FROM tweets WHERE id::text=$1`, Str(ad, "tweet_id")).Scan(&owner) == nil && owner == user {
			items = append(items, ad)
		}
	}
	return Result{Value: map[string]any{"items": items, "campaigns": items, "as_of": time.Now()}}, nil
}
