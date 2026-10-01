package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *Store) AdminUsers(ctx context.Context, q string) ([]map[string]any, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT u.id,u.screen_name,u.name,u.verified,u.premium,u.suspended,u.created_at,COALESCE(u.email,''),COALESCE(p.priority,0) FROM users u LEFT JOIN fl_account_priorities p ON p.user_id=u.id WHERE $1='' OR u.screen_name ILIKE '%'||$1||'%' OR u.name ILIKE '%'||$1||'%' ORDER BY u.id DESC LIMIT 100`, strings.TrimSpace(q))
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name, handle string
		var verified, premium, suspended bool
		var created time.Time
		var email string
		var priority int
		if e = rows.Scan(&id, &handle, &name, &verified, &premium, &suspended, &created, &email, &priority); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": strconv.FormatInt(id, 10), "screen_name": handle, "name": name, "verified": verified, "premium": premium, "suspended": suspended, "created_at": created, "email": email, "priority": priority})
	}
	return out, rows.Err()
}
func (s *Store) SetVerified(ctx context.Context, id string, value bool, actor string) error {
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var name string
	if e = tx.QueryRowContext(ctx, `UPDATE users SET verified=$1 WHERE id=$2::bigint RETURNING screen_name`, value, id).Scan(&name); e == sql.ErrNoRows {
		return fail(404, "user not found")
	} else if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'set_verified',$2,$3)`, actor, id, Encode(map[string]any{"verified": value})); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.invalidateUser(ctx, id, name)
	return nil
}
func (s *Store) Report(ctx context.Context, user string, v map[string]any) (map[string]any, error) {
	kind := Str(v, "target_kind")
	if kind == "" {
		kind = "tweet"
	}
	id := Str(v, "target_id", "tweet_id", "tweetId", "user_id", "trendId", "rest_id")
	reason := strings.TrimSpace(Str(v, "reason", "report_type"))
	details := Str(v, "details", "description")
	if id == "" || reason == "" || len(reason) > 100 || len(details) > 4000 {
		return nil, fail(400, "target_id and reason are required")
	}
	if kind != "tweet" && kind != "user" && kind != "trend" && kind != "community" && kind != "article" {
		return nil, fail(400, "invalid report target")
	}
	if kind == "tweet" || kind == "user" {
		table := "tweets"
		if kind == "user" {
			table = "users"
		}
		var exists bool
		if e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id::text=$1)`, id).Scan(&exists); e != nil {
			return nil, e
		}
		if !exists {
			return nil, fail(404, "report target not found")
		}
	}
	var recent int
	if e := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM fl_reports WHERE reporter_id=$1 AND created_at>now()-interval '1 hour'`, user).Scan(&recent); e != nil {
		return nil, e
	}
	if recent >= 20 {
		return nil, fail(429, "report limit reached")
	}
	var reportID int64
	e := s.DB.QueryRowContext(ctx, `INSERT INTO fl_reports(reporter_id,target_kind,target_id,reason,details) VALUES($1,$2,$3,$4,$5) RETURNING id`, user, kind, id, reason, details).Scan(&reportID)
	return map[string]any{"id": strconv.FormatInt(reportID, 10), "status": "open", "success": e == nil}, e
}
func (s *Store) AdminReports(ctx context.Context, status string) ([]map[string]any, error) {
	if status == "" {
		status = "open"
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT r.id,r.reporter_id,u.screen_name,r.target_kind,r.target_id,r.reason,r.details,r.status,r.decision,r.created_at,r.reviewed_at,COALESCE(t.text,''),COALESCE(target.screen_name,'') FROM fl_reports r LEFT JOIN users u ON u.id=r.reporter_id LEFT JOIN tweets t ON r.target_kind='tweet' AND t.id::text=r.target_id LEFT JOIN users target ON r.target_kind='user' AND target.id::text=r.target_id WHERE $1='all' OR r.status=$1 ORDER BY r.id DESC LIMIT 100`, status)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var reporter sql.NullInt64
		var handle sql.NullString
		var kind, target, reason, details, state, decision, text, targetHandle string
		var created time.Time
		var reviewed sql.NullTime
		if e = rows.Scan(&id, &reporter, &handle, &kind, &target, &reason, &details, &state, &decision, &created, &reviewed, &text, &targetHandle); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": strconv.FormatInt(id, 10), "reporter": handle.String, "target_kind": kind, "target_id": target, "reason": reason, "details": details, "status": state, "decision": decision, "created_at": created, "reviewed_at": reviewed.Time, "tweet_text": text, "target_screen_name": targetHandle})
	}
	return out, rows.Err()
}
func (s *Store) ReviewReport(ctx context.Context, id, action, note, actor string) error {
	if len(note) > 2000 {
		return fail(400, "decision is too long")
	}
	if action != "dismiss" && action != "hide_tweet" && action != "restore_tweet" && action != "suspend_user" && action != "restore_user" {
		return fail(400, "invalid review action")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var kind, target, state string
	e = tx.QueryRowContext(ctx, `SELECT target_kind,target_id,status FROM fl_reports WHERE id=$1::bigint FOR UPDATE`, id).Scan(&kind, &target, &state)
	if e == sql.ErrNoRows {
		return fail(404, "report not found")
	}
	if e != nil {
		return e
	}
	var userID, userName string
	switch action {
	case "hide_tweet", "restore_tweet":
		if kind != "tweet" {
			return fail(400, "report does not target a tweet")
		}
		if _, e = tx.ExecContext(ctx, `UPDATE tweets SET hidden=$1 WHERE id::text=$2`, action == "hide_tweet", target); e != nil {
			return e
		}
	case "suspend_user", "restore_user":
		userID = target
		if kind == "tweet" {
			if e = tx.QueryRowContext(ctx, `SELECT author_id::text FROM tweets WHERE id::text=$1`, target).Scan(&userID); e != nil {
				return e
			}
		} else if kind != "user" {
			return fail(400, "report does not target an account")
		}
		if e = tx.QueryRowContext(ctx, `UPDATE users SET suspended=$1 WHERE id::text=$2 RETURNING screen_name`, action == "suspend_user", userID).Scan(&userName); e != nil {
			return e
		}
	}
	status := "actioned"
	if action == "dismiss" {
		status = "dismissed"
	}
	if _, e = tx.ExecContext(ctx, `UPDATE fl_reports SET status=$1,decision=$2,reviewed_by=$3,reviewed_at=now() WHERE id=$4::bigint`, status, action+": "+note, actor, id); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,$2,$3,$4)`, actor, action, target, Encode(map[string]any{"report_id": id, "note": note})); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	if s.Cache != nil {
		if kind == "tweet" {
			_ = s.Cache.Del(ctx, "tweet:"+target).Err()
		}
		if userID != "" {
			s.invalidateUser(ctx, userID, userName)
		}
	}
	return nil
}
func (s *Store) AdminAds(ctx context.Context) ([]map[string]any, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT a.id,a.name,a.tweet_id,t.text,u.screen_name,a.target_url,a.active,a.starts_at,a.ends_at,a.impression_limit,(SELECT count(*) FROM fl_ad_events e WHERE e.campaign_id=a.id AND kind='impression'),(SELECT count(*) FROM fl_ad_events e WHERE e.campaign_id=a.id AND kind='click') FROM fl_ad_campaigns a JOIN tweets t ON t.id=a.tweet_id JOIN users u ON u.id=t.author_id ORDER BY a.id DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, tweet, cap, impressions, clicks int64
		var name, text, handle, target string
		var active bool
		var start time.Time
		var end sql.NullTime
		if e = rows.Scan(&id, &name, &tweet, &text, &handle, &target, &active, &start, &end, &cap, &impressions, &clicks); e != nil {
			return nil, e
		}
		out = append(out, map[string]any{"id": strconv.FormatInt(id, 10), "name": name, "tweet_id": strconv.FormatInt(tweet, 10), "text": text, "screen_name": handle, "target_url": target, "active": active, "starts_at": start, "ends_at": end.Time, "impression_limit": cap, "impressions": impressions, "clicks": clicks})
	}
	return out, rows.Err()
}
func (s *Store) CreateAd(ctx context.Context, v map[string]any, actor string) (map[string]any, error) {
	name, text, target, handle := Str(v, "name"), Str(v, "text"), Str(v, "target_url"), Str(v, "screen_name")
	if handle == "" {
		handle = "flashlarry"
	}
	link, e := url.Parse(target)
	if e != nil || link.Host == "" || (link.Scheme != "https" && link.Scheme != "http") || link.User != nil {
		return nil, fail(400, "target_url must be an HTTP(S) URL")
	}
	if name == "" || len(name) > 100 || strings.TrimSpace(text) == "" || len([]rune(text)) > 280 {
		return nil, fail(400, "name and text (up to 280 characters) are required")
	}
	cap := Int(v, "impression_limit", 0)
	if cap < 0 {
		return nil, fail(400, "invalid impression limit")
	}
	start := time.Now()
	var end any
	if x := Str(v, "starts_at"); x != "" {
		start, e = parseTime(x)
		if e != nil {
			return nil, fail(400, "invalid starts_at")
		}
	}
	if x := Str(v, "ends_at"); x != "" {
		t, err := parseTime(x)
		if err != nil || !t.After(start) {
			return nil, fail(400, "ends_at must be after starts_at")
		}
		end = t
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var owner, tweet, id int64
	if e = tx.QueryRowContext(ctx, `SELECT id FROM users WHERE lower(screen_name)=lower($1) AND NOT suspended AND NOT protected`, handle).Scan(&owner); e == sql.ErrNoRows {
		return nil, fail(404, "advertiser must be an active public account")
	} else if e != nil {
		return nil, e
	}
	if e = tx.QueryRowContext(ctx, `INSERT INTO tweets(author_id,text,is_ad) VALUES($1,$2,true) RETURNING id`, owner, text).Scan(&tweet); e != nil {
		return nil, e
	}
	if media := Str(v, "media_id"); media != "" {
		res, err := tx.ExecContext(ctx, `INSERT INTO tweet_media(tweet_id,media_id,position) SELECT $1,id,0 FROM media WHERE id::text=$2 AND owner_id=$3::bigint AND status='ready' AND mime LIKE 'image/%'`, tweet, media, owner)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return nil, fail(400, "image must be ready and owned by advertiser")
		}
	}
	if e = tx.QueryRowContext(ctx, `INSERT INTO fl_ad_campaigns(name,tweet_id,target_url,active,starts_at,ends_at,impression_limit) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, name, tweet, target, Bool(v, "active"), start, end, cap).Scan(&id); e != nil {
		return nil, e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'create_ad',$2,$3)`, actor, strconv.FormatInt(id, 10), Encode(v)); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return map[string]any{"id": strconv.FormatInt(id, 10), "tweet_id": strconv.FormatInt(tweet, 10)}, nil
}
func (s *Store) SetAdActive(ctx context.Context, id string, active bool, actor string) error {
	res, e := s.DB.ExecContext(ctx, `UPDATE fl_ad_campaigns SET active=$1 WHERE id::text=$2`, active, id)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fail(404, "campaign not found")
	}
	return s.Audit(ctx, actor, "set_ad_active", id, map[string]any{"active": active})
}
func (s *Store) Catalog(ctx context.Context, kind string, data json.RawMessage, actor string) (map[string]any, error) {
	var v map[string]any
	if json.Unmarshal(data, &v) != nil {
		return nil, fail(400, "JSON object required")
	}
	if kind != "sports" && kind != "space" && kind != "broadcast" && kind != "gif" && kind != "event" && kind != "topic" {
		return nil, fail(400, "invalid catalog kind")
	}
	owner := Str(v, "owner_id")
	if owner == "" {
		_ = s.DB.QueryRowContext(ctx, `SELECT id::text FROM users WHERE screen_name='flashlarry'`).Scan(&owner)
	}
	var id int64
	e := s.DB.QueryRowContext(ctx, `INSERT INTO fl_entities(kind,owner_id,parent_id,data,public) VALUES($1,$2,$3,$4,true) RETURNING id`, kind, owner, Str(v, "parent_id"), data).Scan(&id)
	if e != nil {
		return nil, e
	}
	_ = s.Audit(ctx, actor, "catalog_create", strconv.FormatInt(id, 10), map[string]any{"kind": kind})
	return map[string]any{"id": strconv.FormatInt(id, 10)}, nil
}
