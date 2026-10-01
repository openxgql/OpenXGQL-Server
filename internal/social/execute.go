package social

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"
)

func (s *Store) Execute(ctx context.Context, spec Spec, user string, input map[string]any) (Result, error) {
	if spec.Handler == "excluded" {
		return Result{}, fail(404, "feature excluded: "+spec.Action)
	}
	if !validID(user) {
		return Result{}, fail(401, "authentication required")
	}
	var allowed bool
	if e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1::bigint AND NOT suspended)`, user).Scan(&allowed); e != nil {
		return Result{}, e
	}
	if !allowed {
		return Result{}, fail(403, "account is suspended or missing")
	}
	v := vars(input)
	switch spec.Handler {
	case "edge":
		return s.edge(ctx, spec, user, v)
	case "preference":
		return s.preference(ctx, spec, user, v)
	case "entity":
		return s.entity(ctx, spec, user, v)
	case "bookmark":
		return s.bookmarks(ctx, spec, user, v)
	case "follow":
		return s.follow(ctx, spec, user, v)
	case "follow_list", "relationship_list", "reaction_users", "lookup":
		return s.users(ctx, spec, user, v)
	case "timeline", "search", "discovery":
		return s.timeline(ctx, spec, user, v)
	case "notification":
		return s.notifications(ctx, spec, user, v)
	case "pinned":
		return s.pinned(ctx, spec, user, v)
	case "job":
		return s.jobs(ctx, spec, user, v)
	case "list":
		return s.lists(ctx, spec, user, v)
	case "community":
		return s.communities(ctx, spec, user, v)
	case "article":
		return s.articles(ctx, spec, user, v)
	case "note":
		return s.notes(ctx, spec, user, v)
	case "sports":
		return s.sports(ctx, spec, user, v)
	case "catalog":
		return s.catalog(ctx, spec, user, v)
	case "ads":
		return s.adOperation(ctx, spec, user, v)
	case "account", "utility":
		return s.utility(ctx, spec, user, v)
	case "report":
		v["target_kind"] = "trend"
		value, e := s.Report(ctx, user, v)
		return Result{Value: value}, e
	}
	return Result{}, fail(400, "operation must be executed by its gateway handler")
}
func (s *Store) exists(ctx context.Context, table, id string) error {
	if !validID(id) {
		return fail(400, "invalid target ID")
	}
	var ok bool
	if e := s.DB.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM `+table+` WHERE id=$1::bigint)`, id).Scan(&ok); e != nil {
		return e
	}
	if !ok {
		return fail(404, "target not found")
	}
	return nil
}
func (s *Store) putRelation(ctx context.Context, user, kind, target string, data any) error {
	_, e := s.DB.ExecContext(ctx, `INSERT INTO fl_relations(owner_id,kind,target_id,data) VALUES($1,$2,$3,$4) ON CONFLICT(owner_id,kind,target_id) DO UPDATE SET data=EXCLUDED.data,created_at=now()`, user, kind, target, Encode(data))
	return e
}
func (s *Store) deleteRelation(ctx context.Context, user, kind, target string) error {
	_, e := s.DB.ExecContext(ctx, `DELETE FROM fl_relations WHERE owner_id=$1::bigint AND kind=$2 AND target_id=$3`, user, kind, target)
	return e
}
func (s *Store) relationIDs(ctx context.Context, user, kind string) ([]string, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT target_id FROM fl_relations WHERE owner_id=$1::bigint AND kind=$2 ORDER BY created_at DESC LIMIT 100`, user, kind)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) edge(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	k := spec.Entity
	keys := []string{"tweetId", "tweet_id", "restId", "rest_id", "id"}
	table := "tweets"
	switch k {
	case "block", "mute", "user_alert":
		keys = []string{"targetUserId", "target_user_id", "userId", "user_id", "targetId", "restId"}
		table = "users"
	case "topic_follow", "topic_not_interested", "space_subscription", "space_share", "community_rating", "saved_trend", "pin_timeline":
		keys = []string{"topicId", "topic_id", "spaceId", "audio_space_id", "communityId", "community_id", "listId", "list_id", "timelineId", "timeline_id", "trendId", "trend_id", "id", "restId"}
		table = ""
	}
	target, e := require(v, keys...)
	if e != nil {
		return Result{}, e
	}
	if table != "" {
		if e = s.exists(ctx, table, target); e != nil {
			return Result{}, e
		}
	}
	if k == "block" || k == "mute" {
		if target == user {
			return Result{}, fail(400, "cannot block or mute yourself")
		}
	}
	if k == "pin_tweet" {
		var owner string
		if e = s.DB.QueryRowContext(ctx, `SELECT author_id::text FROM tweets WHERE id=$1::bigint`, target).Scan(&owner); e != nil {
			return Result{}, e
		}
		if owner != user {
			return Result{}, fail(403, "only own posts can be pinned")
		}
	}
	if k == "hide_reply" || k == "pin_reply" {
		var parentOwner sql.NullString
		if e = s.DB.QueryRowContext(ctx, `SELECT p.author_id::text FROM tweets t JOIN tweets p ON p.id=t.reply_to_id WHERE t.id=$1::bigint`, target).Scan(&parentOwner); e == sql.ErrNoRows {
			return Result{}, fail(400, "target is not a reply")
		} else if e != nil {
			return Result{}, e
		}
		if parentOwner.String != user {
			return Result{}, fail(403, "only the conversation author can moderate replies")
		}
	}
	if k == "folder_item" {
		folder, e := require(v, "folderId", "folder_id", "collection_id", "bookmark_collection_id")
		if e != nil {
			return Result{}, e
		}
		if _, e = s.getEntity(ctx, user, "folder", folder, false); e != nil {
			return Result{}, e
		}
		target = folder + ":" + target
	}
	action := spec.Action
	if action == "toggle" {
		enabled := Bool(v, "enabled") || Bool(v, "enable")
		if enabled {
			action = "put"
		} else {
			action = "delete"
		}
	}
	if action == "put" {
		if k == "pin_tweet" {
			tx, err := s.DB.BeginTx(ctx, nil)
			if err != nil {
				return Result{}, err
			}
			defer tx.Rollback()
			if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1::bigint FOR UPDATE`, user); err != nil {
				return Result{}, err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM fl_relations WHERE owner_id=$1::bigint AND kind='pin_tweet'`, user); err != nil {
				return Result{}, err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO fl_relations(owner_id,kind,target_id,data) VALUES($1,'pin_tweet',$2,$3)`, user, target, Encode(v)); err != nil {
				return Result{}, err
			}
			if err = tx.Commit(); err != nil {
				return Result{}, err
			}
			return Result{Value: map[string]any{"success": true, "id": target}, Scalar: "Done"}, nil
		}
		e = s.putRelation(ctx, user, k, target, v)
	} else {
		e = s.deleteRelation(ctx, user, k, target)
	}
	if e != nil {
		return Result{}, e
	}
	if k == "block" && action == "put" {
		_, e = s.DB.ExecContext(ctx, `DELETE FROM follows WHERE (follower_id=$1::bigint AND target_id=$2) OR (follower_id=$2::bigint AND target_id=$1)`, user, target)
		if e != nil {
			return Result{}, e
		}
	}
	value := map[string]any{"success": true, "id": target, "rest_id": target, "__typename": "Success", "enabled": action == "put"}
	if k == "block" {
		value["__typename"] = "BlockSuccessResult"
		if action == "delete" {
			value["__typename"] = "UnBlockSuccessResult"
		}
	}
	if k == "mute" {
		value["__typename"] = "MuteSuccessResult"
		if action == "delete" {
			value["__typename"] = "UnmuteSuccessResult"
		}
	}
	return Result{Value: value, Scalar: "Done"}, nil
}
func (s *Store) preference(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	key := spec.Entity
	if key == "" {
		key = spec.Action
	}
	if key == "conversation_controls" && spec.Kind == "mutation" {
		id, e := require(v, "tweetId", "tweet_id", "restId")
		if e != nil {
			return Result{}, e
		}
		var owner string
		if e = s.DB.QueryRowContext(ctx, `SELECT author_id::text FROM tweets WHERE id::text=$1`, id).Scan(&owner); e != nil {
			return Result{}, e
		}
		if owner != user {
			return Result{}, fail(403, "only the conversation author can change controls")
		}
		key += "/" + id
		if spec.Action == "ConversationControlDelete" {
			_, e = s.DB.ExecContext(ctx, `DELETE FROM fl_preferences WHERE owner_id=$1::bigint AND key=$2`, user, key)
			return Result{Value: map[string]any{"success": e == nil}, Scalar: "Done"}, e
		}
	}
	if spec.Kind == "mutation" {
		if spec.Action == "DisableUserAccountLabel" {
			v["label"] = nil
			v["enabled"] = false
		}
		if key == "phone_label" {
			v["enabled"] = spec.Action == "EnableVerifiedPhoneLabel" || spec.Action == "verifiedPhoneRowEnableMutation"
		}
		if key == "preferences" && v["protected"] != nil {
			protected, ok := v["protected"].(bool)
			if !ok {
				return Result{}, fail(400, "protected must be boolean")
			}
			if _, e := s.DB.ExecContext(ctx, `UPDATE users SET protected=$1 WHERE id=$2::bigint`, protected, user); e != nil {
				return Result{}, e
			}
		}
		if _, e := s.DB.ExecContext(ctx, `INSERT INTO fl_preferences(owner_id,key,data) VALUES($1,$2,$3) ON CONFLICT(owner_id,key) DO UPDATE SET data=fl_preferences.data||EXCLUDED.data,updated_at=now()`, user, key, Encode(v)); e != nil {
			return Result{}, e
		}
	}
	var raw []byte
	e := s.DB.QueryRowContext(ctx, `SELECT data FROM fl_preferences WHERE owner_id=$1::bigint AND key=$2`, user, key).Scan(&raw)
	if e != nil && e != sql.ErrNoRows {
		return Result{}, e
	}
	value := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &value)
	}
	value["success"] = true
	value["settings"] = object(value["settings"])
	return Result{Value: value, Scalar: "Done", UserID: user}, nil
}
func typename(kind string) string {
	switch kind {
	case "folder":
		return "BookmarkCollection"
	case "list":
		return "List"
	case "community":
		return "Community"
	case "article":
		return "ArticleEntity"
	case "note":
		return "BirdwatchNote"
	case "space":
		return "AudioSpace"
	case "broadcast":
		return "Broadcast"
	}
	return "Entity"
}
func (s *Store) getEntity(ctx context.Context, user, kind, id string, allowPublic bool) (map[string]any, error) {
	var raw []byte
	var owner string
	var created, updated time.Time
	var public bool
	e := s.DB.QueryRowContext(ctx, `SELECT data,owner_id::text,created_at,updated_at,public FROM fl_entities WHERE id::text=$1 AND kind=$2 AND deleted_at IS NULL AND (owner_id=$3::bigint OR ($4 AND public))`, id, kind, user, allowPublic).Scan(&raw, &owner, &created, &updated, &public)
	if e == sql.ErrNoRows {
		return nil, fail(404, "entity not found or inaccessible")
	}
	if e != nil {
		return nil, e
	}
	value := map[string]any{}
	_ = json.Unmarshal(raw, &value)
	value["id"] = id
	value["rest_id"] = id
	value["owner_id"] = owner
	value["__typename"] = typename(kind)
	value["created_at"] = created
	value["updated_at"] = updated
	value["public"] = public
	value["accessibility"] = "Public"
	if !public {
		value["accessibility"] = "Private"
	}
	value["mode"] = value["accessibility"]
	value["owner_results"] = map[string]any{"result": map[string]any{"__user_id": owner}}
	value["user_results"] = map[string]any{"result": map[string]any{"__user_id": owner}}
	return value, nil
}
func (s *Store) listEntities(ctx context.Context, user, kind, parent, q string, public bool) ([]map[string]any, error) {
	rows, e := s.DB.QueryContext(ctx, `SELECT id::text FROM fl_entities WHERE kind=$1 AND deleted_at IS NULL AND (owner_id=$2::bigint OR ($3 AND public)) AND ($4='' OR parent_id=$4) AND ($5='' OR data::text ILIKE '%'||$5||'%') ORDER BY id DESC LIMIT 100`, kind, user, public, parent, q)
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return nil, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	items := []map[string]any{}
	for _, id := range ids {
		item, e := s.getEntity(ctx, user, kind, id, public)
		if e != nil {
			return nil, e
		}
		items = append(items, item)
	}
	return items, nil
}
func (s *Store) createEntity(ctx context.Context, user, kind, parent string, v map[string]any, public bool) (map[string]any, error) {
	var id int64
	if len(Encode(v)) > 1<<18 {
		return nil, fail(400, "entity is too large")
	}
	e := s.DB.QueryRowContext(ctx, `INSERT INTO fl_entities(kind,owner_id,parent_id,data,public) VALUES($1,$2,$3,$4,$5) RETURNING id`, kind, user, parent, Encode(v), public).Scan(&id)
	if e != nil {
		return nil, e
	}
	return s.getEntity(ctx, user, kind, strconv.FormatInt(id, 10), false)
}
func (s *Store) updateEntity(ctx context.Context, user, kind, id string, v map[string]any, remove bool) (map[string]any, error) {
	if _, e := s.getEntity(ctx, user, kind, id, false); e != nil {
		return nil, e
	}
	if remove {
		_, e := s.DB.ExecContext(ctx, `UPDATE fl_entities SET deleted_at=now(),updated_at=now() WHERE id::text=$1 AND owner_id=$2::bigint AND kind=$3`, id, user, kind)
		return map[string]any{"id": id, "rest_id": id, "success": e == nil}, e
	}
	_, e := s.DB.ExecContext(ctx, `UPDATE fl_entities SET data=data||$1,updated_at=now() WHERE id::text=$2 AND owner_id=$3::bigint AND kind=$4`, Encode(v), id, user, kind)
	if e != nil {
		return nil, e
	}
	if kind == "list" {
		var public *bool
		if p, ok := v["is_private"].(bool); ok {
			x := !p
			public = &x
		}
		if p, ok := v["isPrivate"].(bool); ok {
			x := !p
			public = &x
		}
		if mode := Str(v, "mode"); mode != "" {
			x := mode != "private" && mode != "Private"
			public = &x
		}
		if public != nil {
			if _, e = s.DB.ExecContext(ctx, `UPDATE fl_entities SET public=$1 WHERE id::text=$2 AND owner_id=$3::bigint`, *public, id, user); e != nil {
				return nil, e
			}
		}
	}
	return s.getEntity(ctx, user, kind, id, false)
}
func (s *Store) entity(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	var value map[string]any
	var e error
	switch spec.Action {
	case "create":
		name, err := require(v, "name", "folderName", "folder_name")
		if err != nil {
			return Result{}, err
		}
		if len([]rune(name)) > 100 {
			return Result{}, fail(400, "name too long")
		}
		v["name"] = name
		value, e = s.createEntity(ctx, user, spec.Entity, "", v, false)
	case "update", "delete", "get":
		id, err := require(v, "folderId", "folder_id", "collection_id", "bookmark_collection_id", "id", "restId")
		if err != nil {
			return Result{}, err
		}
		if spec.Action == "get" {
			value, e = s.getEntity(ctx, user, spec.Entity, id, false)
		} else {
			value, e = s.updateEntity(ctx, user, spec.Entity, id, v, spec.Action == "delete")
		}
	case "list":
		items, err := s.listEntities(ctx, user, spec.Entity, "", "", false)
		e = err
		value = map[string]any{"items": items, "items_results": items, "slice": map[string]any{"items": items, "items_results": items}, "next_cursor": nil}
	}
	return Result{Value: value, Scalar: "Done"}, e
}
func (s *Store) bookmarks(ctx context.Context, spec Spec, user string, v map[string]any) (Result, error) {
	if spec.Action == "BookmarksAllDelete" {
		_, e := s.DB.ExecContext(ctx, `DELETE FROM fl_relations WHERE owner_id=$1::bigint AND kind IN ('bookmark','folder_item')`, user)
		return Result{Value: map[string]any{"success": e == nil}, Scalar: "Done"}, e
	}
	kind := "bookmark"
	folder := Str(v, "folderId", "folder_id", "collection_id", "bookmark_collection_id")
	if spec.Action == "BookmarkFolderTimeline" {
		if _, e := s.getEntity(ctx, user, "folder", folder, false); e != nil {
			return Result{}, e
		}
		kind = "folder_item"
	}
	q := Str(v, "rawQuery", "raw_query", "query")
	rows, e := s.DB.QueryContext(ctx, `SELECT t.id::text FROM fl_relations r JOIN tweets t ON t.id::text=CASE WHEN r.kind='folder_item' THEN split_part(r.target_id,':',2) ELSE r.target_id END JOIN users u ON u.id=t.author_id WHERE r.owner_id=$1::bigint AND r.kind=$2 AND ($3='' OR split_part(r.target_id,':',1)=$3) AND NOT t.hidden AND NOT u.suspended AND ($4='' OR t.text ILIKE '%'||$4||'%') ORDER BY r.created_at DESC LIMIT $5`, user, kind, folder, q, limit(v))
	ids, e := scanIDs(rows, e)
	return Result{TweetIDs: ids, Timeline: true, Value: map[string]any{"id": "bookmarks-" + user}}, e
}
func scanIDs(rows *sql.Rows, e error) ([]string, error) {
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return nil, e
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
