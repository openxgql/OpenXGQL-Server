package social

import (
	"context"
	"database/sql"
	"net/mail"
	"strings"
)

func (s *Store) SetSuspended(ctx context.Context, id string, value bool, actor string) error {
	if !validID(id) {
		return fail(400, "invalid user ID")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var name string
	if e = tx.QueryRowContext(ctx, `UPDATE users SET suspended=$1 WHERE id=$2::bigint RETURNING screen_name`, value, id).Scan(&name); e == sql.ErrNoRows {
		return fail(404, "user not found")
	} else if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'set_suspended',$2,$3)`, actor, id, Encode(map[string]any{"suspended": value})); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.invalidateUser(ctx, id, name)
	return nil
}
func (s *Store) SetPremium(ctx context.Context, id string, value bool, actor string) error {
	if !validID(id) {
		return fail(400, "invalid user ID")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var name string
	if e = tx.QueryRowContext(ctx, `UPDATE users SET premium=$1 WHERE id=$2::bigint RETURNING screen_name`, value, id).Scan(&name); e == sql.ErrNoRows {
		return fail(404, "user not found")
	} else if e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'set_premium',$2,$3)`, actor, id, Encode(map[string]any{"premium": value})); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.invalidateUser(ctx, id, name)
	return nil
}
func (s *Store) SetPriority(ctx context.Context, id string, priority int, actor string) error {
	if priority < -100 || priority > 100 {
		return fail(400, "priority must be between -100 and 100")
	}
	if e := s.exists(ctx, "users", id); e != nil {
		return e
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_account_priorities(user_id,priority) VALUES($1::bigint,$2) ON CONFLICT(user_id) DO UPDATE SET priority=EXCLUDED.priority,updated_at=now()`, id, priority); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'set_priority',$2,$3)`, actor, id, Encode(map[string]any{"priority": priority})); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) SetEmail(ctx context.Context, id, email, actor string) error {
	if !validID(id) {
		return fail(400, "invalid user ID")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	parsed, e := mail.ParseAddress(email)
	if e != nil || parsed.Address != email || len(email) > 254 {
		return fail(400, "valid email required")
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var name string
	if e = tx.QueryRowContext(ctx, `UPDATE users SET email=$1 WHERE id=$2::bigint RETURNING screen_name`, email, id).Scan(&name); e == sql.ErrNoRows {
		return fail(404, "user not found")
	} else if e != nil {
		return fail(409, "email is already used or cannot be saved")
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO fl_audit(actor,action,target,data) VALUES($1,'set_email',$2,'{}')`, actor, id); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	s.invalidateUser(ctx, id, name)
	return nil
}
