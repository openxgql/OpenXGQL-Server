package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"flashlarry-api/internal/siteconfig"

	"github.com/redis/go-redis/v9"
)

type Store struct {
	DB    *sql.DB
	Cache *redis.Client
}
type Error struct {
	Status  int
	Message string
}

func (e *Error) Error() string              { return e.Message }
func fail(status int, message string) error { return &Error{status, message} }
func (s *Store) Init(ctx context.Context) error {
	if _, err := s.DB.ExecContext(ctx, siteconfig.Schema); err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, feedSchema); err != nil {
		return err
	}
	_, err := s.DB.ExecContext(ctx, `
 ALTER TABLE users ADD COLUMN IF NOT EXISTS suspended BOOLEAN NOT NULL DEFAULT false;
 ALTER TABLE users ADD COLUMN IF NOT EXISTS protected BOOLEAN NOT NULL DEFAULT false;
 ALTER TABLE tweets ADD COLUMN IF NOT EXISTS hidden BOOLEAN NOT NULL DEFAULT false;
 ALTER TABLE tweets ADD COLUMN IF NOT EXISTS is_ad BOOLEAN NOT NULL DEFAULT false;
 CREATE TABLE IF NOT EXISTS fl_entities (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, kind TEXT NOT NULL, owner_id BIGINT NOT NULL REFERENCES users(id), parent_id TEXT NOT NULL DEFAULT '', data JSONB NOT NULL DEFAULT '{}', public BOOLEAN NOT NULL DEFAULT false, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), deleted_at TIMESTAMPTZ);
 CREATE INDEX IF NOT EXISTS fl_entities_kind_owner ON fl_entities(kind,owner_id,id DESC) WHERE deleted_at IS NULL;
 CREATE TABLE IF NOT EXISTS fl_relations (
 owner_id BIGINT NOT NULL REFERENCES users(id), kind TEXT NOT NULL, target_id TEXT NOT NULL, data JSONB NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(owner_id,kind,target_id));
 CREATE UNIQUE INDEX IF NOT EXISTS fl_one_pinned_tweet ON fl_relations(owner_id,kind) WHERE kind='pin_tweet';
 CREATE INDEX IF NOT EXISTS fl_relations_target ON fl_relations(kind,target_id,created_at DESC);
 CREATE TABLE IF NOT EXISTS fl_preferences (owner_id BIGINT NOT NULL REFERENCES users(id), key TEXT NOT NULL, data JSONB NOT NULL, updated_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(owner_id,key));
 CREATE TABLE IF NOT EXISTS fl_reports (
 id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, reporter_id BIGINT REFERENCES users(id), target_kind TEXT NOT NULL CHECK(target_kind IN ('tweet','user','trend','community','article')), target_id TEXT NOT NULL, reason TEXT NOT NULL, details TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT 'open' CHECK(status IN ('open','dismissed','actioned')), decision TEXT NOT NULL DEFAULT '', reviewed_by TEXT, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), reviewed_at TIMESTAMPTZ);
 CREATE INDEX IF NOT EXISTS fl_reports_status ON fl_reports(status,id DESC);
 CREATE TABLE IF NOT EXISTS fl_audit (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, actor TEXT NOT NULL, action TEXT NOT NULL, target TEXT NOT NULL, data JSONB NOT NULL DEFAULT '{}', created_at TIMESTAMPTZ NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS fl_notifications (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, user_id BIGINT NOT NULL REFERENCES users(id), actor_id BIGINT NOT NULL REFERENCES users(id), kind TEXT NOT NULL, tweet_id BIGINT REFERENCES tweets(id) ON DELETE CASCADE, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), read_at TIMESTAMPTZ);
 CREATE INDEX IF NOT EXISTS fl_notifications_user ON fl_notifications(user_id,id DESC);
 CREATE TABLE IF NOT EXISTS fl_ad_campaigns (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name TEXT NOT NULL, tweet_id BIGINT NOT NULL REFERENCES tweets(id) ON DELETE CASCADE, target_url TEXT NOT NULL, active BOOLEAN NOT NULL DEFAULT false, starts_at TIMESTAMPTZ NOT NULL DEFAULT now(), ends_at TIMESTAMPTZ, impression_limit BIGINT NOT NULL DEFAULT 0 CHECK(impression_limit>=0), created_at TIMESTAMPTZ NOT NULL DEFAULT now());
 CREATE TABLE IF NOT EXISTS fl_ad_events (campaign_id BIGINT NOT NULL REFERENCES fl_ad_campaigns(id) ON DELETE CASCADE, viewer_id BIGINT REFERENCES users(id), delivery_id TEXT NOT NULL, kind TEXT NOT NULL CHECK(kind IN ('impression','click')), created_at TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY(delivery_id,kind));
 ALTER TABLE fl_ad_events DROP CONSTRAINT IF EXISTS fl_ad_events_campaign_id_fkey;
 ALTER TABLE fl_ad_events ADD CONSTRAINT fl_ad_events_campaign_id_fkey FOREIGN KEY(campaign_id) REFERENCES fl_ad_campaigns(id) ON DELETE CASCADE;
 ALTER TABLE fl_ad_campaigns DROP CONSTRAINT IF EXISTS fl_ad_campaigns_tweet_id_fkey;
 ALTER TABLE fl_ad_campaigns ADD CONSTRAINT fl_ad_campaigns_tweet_id_fkey FOREIGN KEY(tweet_id) REFERENCES tweets(id) ON DELETE CASCADE;
 CREATE TABLE IF NOT EXISTS fl_jobs (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, owner_id BIGINT NOT NULL REFERENCES users(id), kind TEXT NOT NULL, data JSONB NOT NULL, due_at TIMESTAMPTZ, status TEXT NOT NULL DEFAULT 'draft', tweet_id BIGINT REFERENCES tweets(id) ON DELETE SET NULL, created_at TIMESTAMPTZ NOT NULL DEFAULT now(), updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
 CREATE OR REPLACE FUNCTION fl_notify() RETURNS trigger LANGUAGE plpgsql AS $$
 DECLARE recipient bigint; post bigint; kind text;
 BEGIN
 IF TG_TABLE_NAME='follows' THEN recipient=NEW.target_id;kind='follow';
 ELSIF TG_TABLE_NAME='reactions' THEN SELECT author_id INTO recipient FROM tweets WHERE id=NEW.tweet_id;post=NEW.tweet_id;kind='like';
 ELSIF TG_TABLE_NAME='retweets' THEN SELECT author_id INTO recipient FROM tweets WHERE id=NEW.tweet_id;post=NEW.tweet_id;kind='repost';
 ELSE IF NEW.reply_to_id IS NULL THEN RETURN NEW;END IF; SELECT author_id INTO recipient FROM tweets WHERE id=NEW.reply_to_id;post=NEW.id;kind='reply';END IF;
 IF recipient IS NOT NULL THEN
 IF TG_TABLE_NAME='follows' THEN IF recipient<>NEW.follower_id THEN INSERT INTO fl_notifications(user_id,actor_id,kind,tweet_id) VALUES(recipient,NEW.follower_id,kind,post);END IF;
 ELSIF TG_TABLE_NAME='tweets' THEN IF recipient<>NEW.author_id THEN INSERT INTO fl_notifications(user_id,actor_id,kind,tweet_id) VALUES(recipient,NEW.author_id,kind,post);END IF;
 ELSE IF recipient<>NEW.user_id THEN INSERT INTO fl_notifications(user_id,actor_id,kind,tweet_id) VALUES(recipient,NEW.user_id,kind,post);END IF;END IF;
 END IF; RETURN NEW;END $$;
 DROP TRIGGER IF EXISTS fl_notify_follows ON follows;CREATE TRIGGER fl_notify_follows AFTER INSERT ON follows FOR EACH ROW EXECUTE FUNCTION fl_notify();
 DROP TRIGGER IF EXISTS fl_notify_reactions ON reactions;CREATE TRIGGER fl_notify_reactions AFTER INSERT ON reactions FOR EACH ROW EXECUTE FUNCTION fl_notify();
 DROP TRIGGER IF EXISTS fl_notify_retweets ON retweets;CREATE TRIGGER fl_notify_retweets AFTER INSERT ON retweets FOR EACH ROW EXECUTE FUNCTION fl_notify();
 DROP TRIGGER IF EXISTS fl_notify_tweets ON tweets;CREATE TRIGGER fl_notify_tweets AFTER INSERT ON tweets FOR EACH ROW EXECUTE FUNCTION fl_notify();`)
	return err
}
func Encode(v any) []byte { b, _ := json.Marshal(v); return b }
func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}
func Str(v map[string]any, keys ...string) string {
	for _, k := range keys {
		switch x := v[k].(type) {
		case string:
			if x != "" {
				return x
			}
		case float64:
			return strconv.FormatInt(int64(x), 10)
		case json.Number:
			return string(x)
		}
	}
	return ""
}
func Int(v map[string]any, key string, fallback int) int {
	x := Str(v, key)
	n, e := strconv.Atoi(x)
	if e != nil {
		return fallback
	}
	return n
}
func Bool(v map[string]any, key string) bool { x, _ := v[key].(bool); return x }
func vars(v map[string]any) map[string]any {
	out := map[string]any{}
	for k, x := range v {
		out[k] = x
	}
	for _, key := range []string{"input", "request", "data", "post_request", "tweet_info"} {
		for k, x := range object(v[key]) {
			if _, ok := out[k]; !ok {
				out[k] = x
			}
		}
	}
	return out
}
func validID(id string) bool { n, e := strconv.ParseInt(id, 10, 64); return e == nil && n > 0 }
func limit(v map[string]any) int {
	n := Int(v, "count", 20)
	if n < 1 {
		n = 20
	}
	if n > 100 {
		n = 100
	}
	return n
}
func (s *Store) Audit(ctx context.Context, actor, action, target string, data any) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,$2,$3,$4)`, actor, action, target, Encode(data))
	return e
}
func (s *Store) invalidateUser(ctx context.Context, id, name string) {
	if s.Cache != nil {
		_ = s.Cache.Del(ctx, "user:id:"+id, "user:name:"+strings.ToLower(name)).Err()
	}
}
func ErrorStatus(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return http.StatusInternalServerError
}
func ErrorMessage(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "database operation failed"
}
func parseTime(v string) (time.Time, error) {
	if n, e := strconv.ParseInt(v, 10, 64); e == nil {
		if n > 1e12 {
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}
	return time.Parse(time.RFC3339, v)
}
func require(v map[string]any, keys ...string) (string, error) {
	x := Str(v, keys...)
	if x == "" {
		return "", fail(400, fmt.Sprintf("variables.%s is required", keys[0]))
	}
	return x, nil
}
