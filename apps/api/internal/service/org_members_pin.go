package service

import (
	"context"
	"database/sql"
	"net/http"
)

// ResetLoginPin clears a member's or mailbox user's login PIN after they
// forgot it. Owners reset anyone but themselves; admins reset members and
// mailbox users only (never the owner or another admin). Every session,
// pending challenge and remembered device ends; the next sign-in asks for a
// new PIN. Audited. 409 when the user has no PIN.
func (s *OrgMemberService) ResetLoginPin(ctx context.Context, a OrgActor, userUUID string) (*OrgMember, error) {
	if !validUUID(userUUID) {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var id int64
	var email, role, status string
	var hasPin bool
	err = tx.QueryRowContext(ctx, `SELECT id,email,role,status,login_pin_hash IS NOT NULL FROM users WHERE uuid=$1 AND org_id=$2 FOR NO KEY UPDATE`, userUUID, a.OrgID).
		Scan(&id, &email, &role, &status, &hasPin)
	if err == sql.ErrNoRows || (err == nil && status != "active" && status != "suspended") {
		return nil, orgError(http.StatusNotFound, "Member not found")
	}
	if err != nil {
		return nil, err
	}
	if id == a.UserID {
		return nil, orgError(http.StatusConflict, "Change your own PIN in Settings → Security")
	}
	if role == "owner" || (role == "admin" && a.Role != "owner") {
		return nil, orgError(http.StatusForbidden, "Only the organization owner can reset an admin's PIN, and nobody can reset the owner's")
	}
	if !hasPin {
		return nil, orgError(http.StatusConflict, "This user has not set a login PIN yet")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE users SET login_pin_hash=NULL,login_pin_set_at=NULL,auth_version=auth_version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return nil, err
	}
	n, err := revokeSignIns(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	// The PIN itself is cleared, so its wrong-PIN counts end here too.
	if err = clearPinFailures(ctx, tx, id); err != nil {
		return nil, err
	}
	if err = auditTx(ctx, tx, a, "login_pin_reset", "user", userUUID, "Reset the login PIN of "+email, map[string]any{"role": role, "sessionsRevoked": n}); err != nil {
		return nil, err
	}
	m, err := s.member(ctx, tx, a.OrgID, userUUID)
	if err != nil {
		return nil, err
	}
	return m, tx.Commit()
}
