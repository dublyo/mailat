package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

var (
	ErrInvalidMailboxInput  = errors.New("invalid mailbox input")
	ErrMailboxNotFound      = errors.New("message or resource not found")
	ErrMailboxConflict      = errors.New("resource is currently sending or conflicts with an existing resource")
	ErrMailboxCursorExpired = errors.New("cursor has expired; resynchronize the mailbox")
)

func (s *InboxService) ListLabels(ctx context.Context, userID int64) ([]model.EmailLabel, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,uuid,org_id,user_id,name,color,created_at,updated_at FROM email_labels WHERE user_id=$1 ORDER BY name,id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	labels := []model.EmailLabel{}
	for rows.Next() {
		var l model.EmailLabel
		if err = rows.Scan(&l.ID, &l.UUID, &l.OrgID, &l.UserID, &l.Name, &l.Color, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, err
		}
		labels = append(labels, l)
	}
	return labels, rows.Err()
}

func (s *InboxService) SaveLabel(ctx context.Context, orgID, userID int64, id, name, color string) (*model.EmailLabel, error) {
	name = strings.TrimSpace(name)
	if id != "" {
		if _, err := uuid.Parse(id); err != nil {
			return nil, ErrInvalidMailboxInput
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockMailboxLabels(ctx, tx, userID); err != nil {
		return nil, err
	}
	oldName := ""
	if id != "" {
		var oldColor string
		err = tx.QueryRowContext(ctx, `SELECT name,color FROM email_labels WHERE uuid=$1 AND user_id=$2 FOR UPDATE`, id, userID).Scan(&oldName, &oldColor)
		if err == sql.ErrNoRows {
			return nil, ErrMailboxNotFound
		}
		if err != nil {
			return nil, err
		}
		if name == "" {
			name = oldName
		}
		if color == "" {
			color = oldColor
		}
	}
	if len(name) < 1 || len(name) > 100 {
		return nil, fmt.Errorf("%w: label name must be 1 to 100 characters", ErrInvalidMailboxInput)
	}
	if color == "" {
		color = "#6366f1"
	}
	if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(color) {
		return nil, fmt.Errorf("%w: use a six-digit hex color", ErrInvalidMailboxInput)
	}
	var l model.EmailLabel
	if id == "" {
		err = tx.QueryRowContext(ctx, `INSERT INTO email_labels(org_id,user_id,name,color,updated_at) VALUES($1,$2,$3,$4,NOW()) RETURNING id,uuid,org_id,user_id,name,color,created_at,updated_at`, orgID, userID, name, color).Scan(&l.ID, &l.UUID, &l.OrgID, &l.UserID, &l.Name, &l.Color, &l.CreatedAt, &l.UpdatedAt)
	} else {
		err = tx.QueryRowContext(ctx, `UPDATE email_labels SET name=$3,color=$4,updated_at=NOW() WHERE uuid=$1 AND user_id=$2 RETURNING id,uuid,org_id,user_id,name,color,created_at,updated_at`, id, userID, name, color).Scan(&l.ID, &l.UUID, &l.OrgID, &l.UserID, &l.Name, &l.Color, &l.CreatedAt, &l.UpdatedAt)
	}
	if err != nil {
		var pg *pq.Error
		if errors.As(err, &pg) && pg.Code == "23505" {
			return nil, ErrMailboxConflict
		}
		return nil, err
	}
	if oldName != "" && oldName != name {
		if _, err = tx.ExecContext(ctx, `UPDATE received_emails SET labels=array_replace(labels,$2,$3),updated_at=NOW() WHERE mailbox_owner_id=$1 AND $2=ANY(labels)`, userID, oldName, name); err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE inbox_filters SET action_labels=array_replace(action_labels,$2,$3),updated_at=NOW() WHERE user_id=$1 AND $2=ANY(action_labels)`, userID, oldName, name); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &l, nil
}

func (s *InboxService) DeleteLabel(ctx context.Context, userID int64, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrInvalidMailboxInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockMailboxLabels(ctx, tx, userID); err != nil {
		return err
	}
	var name string
	err = tx.QueryRowContext(ctx, `DELETE FROM email_labels WHERE uuid=$1 AND user_id=$2 RETURNING name`, id, userID).Scan(&name)
	if err == sql.ErrNoRows {
		return ErrMailboxNotFound
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE received_emails SET labels=array_remove(labels,$2),updated_at=NOW() WHERE mailbox_owner_id=$1 AND $2=ANY(labels)`, userID, name); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE inbox_filters SET action_labels=array_remove(action_labels,$2),updated_at=NOW() WHERE user_id=$1 AND $2=ANY(action_labels)`, userID, name); err != nil {
		return err
	}
	return tx.Commit()
}

type mailboxQuery interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func validateLabels(ctx context.Context, q mailboxQuery, userID int64, names []string) error {
	if len(names) > 50 {
		return fmt.Errorf("%w: at most 50 labels", ErrInvalidMailboxInput)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			return fmt.Errorf("%w: duplicate label", ErrInvalidMailboxInput)
		}
		seen[name] = true
		var exists bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM email_labels WHERE user_id=$1 AND name=$2)`, userID, name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: unknown label", ErrInvalidMailboxInput)
		}
	}
	return nil
}
func (s *InboxService) LabelReceivedEmails(ctx context.Context, userID int64, ids, add, remove []string) error {
	if add == nil {
		add = []string{}
	}
	if remove == nil {
		remove = []string{}
	}
	tx, err := s.ownedMessageTransaction(ctx, userID, ids)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = validateLabels(ctx, tx, userID, add); err != nil {
		return err
	}
	if err = validateLabels(ctx, tx, userID, remove); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE received_emails SET labels=ARRAY(SELECT DISTINCT value FROM unnest(COALESCE(labels,'{}')||$1::text[]) AS value WHERE NOT(value=ANY($2::text[])) ORDER BY value),updated_at=NOW() WHERE uuid=ANY($3::uuid[])`, pq.Array(add), pq.Array(remove), pq.Array(ids))
	if err != nil {
		return err
	}
	return tx.Commit()
}

const filterColumns = `id,uuid,org_id,user_id,identity_id,name,kind,priority,active,conditions,condition_logic,COALESCE(action_labels,'{}'),COALESCE(action_folder,''),action_star,action_mark_read,action_archive,action_trash,COALESCE(action_forward,''),match_count,last_matched_at,created_at,updated_at`

type rowScanner interface{ Scan(...interface{}) error }

func scanInboxFilter(row rowScanner) (model.InboxFilter, error) {
	var f model.InboxFilter
	var conditions []byte
	err := row.Scan(&f.ID, &f.UUID, &f.OrgID, &f.UserID, &f.IdentityID, &f.Name, &f.Kind, &f.Priority, &f.Active, &conditions, &f.ConditionLogic, pq.Array(&f.ActionLabels), &f.ActionFolder, &f.ActionStar, &f.ActionMarkRead, &f.ActionArchive, &f.ActionTrash, &f.ActionForward, &f.MatchCount, &f.LastMatchedAt, &f.CreatedAt, &f.UpdatedAt)
	if err == nil {
		err = json.Unmarshal(conditions, &f.Conditions)
	}
	return f, err
}

// ListFilters returns the user's filters in evaluation order. kind optionally
// narrows the list to "filter" or "blocked_sender".
func (s *InboxService) ListFilters(ctx context.Context, userID int64, kind string) ([]model.InboxFilter, error) {
	if kind != "" && kind != FilterKindFilter && kind != FilterKindBlockedSender {
		return nil, fmt.Errorf("%w: kind must be filter or blocked_sender", ErrInvalidMailboxInput)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+filterColumns+` FROM inbox_filters WHERE user_id=$1 AND ($2='' OR kind=$2) ORDER BY (kind='blocked_sender'),priority DESC,id`, userID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.InboxFilter{}
	for rows.Next() {
		f, err := scanInboxFilter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
func (s *InboxService) GetFilter(ctx context.Context, userID int64, id string) (*model.InboxFilter, error) {
	if _, err := uuid.Parse(id); err != nil {
		return nil, ErrInvalidMailboxInput
	}
	f, err := scanInboxFilter(s.db.QueryRowContext(ctx, `SELECT `+filterColumns+` FROM inbox_filters WHERE user_id=$1 AND uuid=$2`, userID, id))
	if err == sql.ErrNoRows {
		return nil, ErrMailboxNotFound
	}
	return &f, err
}

func (s *InboxService) ValidateFilter(ctx context.Context, userID int64, f *model.InboxFilter) error {
	return s.validateFilter(ctx, s.db, userID, f)
}
func (s *InboxService) validateFilter(ctx context.Context, q mailboxQuery, userID int64, f *model.InboxFilter) error {
	switch f.Kind {
	case "":
		f.Kind = FilterKindFilter
	case FilterKindFilter:
	case FilterKindBlockedSender:
		if err := normalizeBlockedSender(f); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: kind must be filter or blocked_sender", ErrInvalidMailboxInput)
	}
	f.Name = strings.TrimSpace(f.Name)
	if len(f.Name) < 1 || len(f.Name) > 255 || f.Priority < -10000 || f.Priority > 10000 {
		return fmt.Errorf("%w: invalid filter name or priority", ErrInvalidMailboxInput)
	}
	if f.IdentityID != nil {
		if *f.IdentityID < 1 {
			return ErrInvalidMailboxInput
		}
		var owned bool
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM identities i WHERE i.id=$1 AND `+identityAccessSQL("i", "$2", identityCanRead)+`)`, *f.IdentityID, userID).Scan(&owned); err != nil {
			return err
		}
		if !owned {
			return ErrMailboxNotFound
		}
	}
	if f.ConditionLogic != "all" && f.ConditionLogic != "any" {
		return fmt.Errorf("%w: conditionLogic must be all or any", ErrInvalidMailboxInput)
	}
	if len(f.Conditions) == 0 || len(f.Conditions) > 25 {
		return fmt.Errorf("%w: provide 1 to 25 conditions", ErrInvalidMailboxInput)
	}
	for _, c := range f.Conditions {
		switch c.Field {
		case "from", "to", "subject", "body", "hasAttachment":
		default:
			return fmt.Errorf("%w: unsupported condition field", ErrInvalidMailboxInput)
		}
		switch c.Operator {
		case "contains", "equals", "startsWith", "endsWith", "regex", "notContains", "notEquals":
		default:
			return fmt.Errorf("%w: unsupported condition operator", ErrInvalidMailboxInput)
		}
		if len(c.Value) > 1000 {
			return fmt.Errorf("%w: condition too long", ErrInvalidMailboxInput)
		}
		if c.Field == "hasAttachment" && (c.Operator != "equals" || (c.Value != "true" && c.Value != "false")) {
			return fmt.Errorf("%w: attachment condition requires equals true or false", ErrInvalidMailboxInput)
		}
		if c.Operator == "regex" {
			if _, err := regexp.Compile(c.Value); err != nil {
				return fmt.Errorf("%w: invalid regular expression", ErrInvalidMailboxInput)
			}
		}
	}
	switch f.ActionFolder {
	case "", "inbox", "archive", "spam", "trash", DMARCReportsFolder:
	default:
		return fmt.Errorf("%w: invalid destination folder", ErrInvalidMailboxInput)
	}
	if f.ActionForward != "" {
		return fmt.Errorf("%w: use the forwarding API in your workflow; automatic forwarding is not a receiving filter action", ErrInvalidMailboxInput)
	}
	if f.ActionFolder == "" && !f.ActionStar && !f.ActionMarkRead && !f.ActionArchive && !f.ActionTrash && len(f.ActionLabels) == 0 {
		return fmt.Errorf("%w: provide at least one filter action", ErrInvalidMailboxInput)
	}
	return validateLabels(ctx, q, userID, f.ActionLabels)
}
func (s *InboxService) SaveFilter(ctx context.Context, orgID, userID int64, id string, f *model.InboxFilter) (*model.InboxFilter, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err = lockMailboxLabels(ctx, tx, userID); err != nil {
		return nil, err
	}
	if err := s.validateFilter(ctx, tx, userID, f); err != nil {
		return nil, err
	}
	body, err := json.Marshal(f.Conditions)
	if err != nil {
		return nil, err
	}
	args := []interface{}{userID, orgID, f.IdentityID, f.Name, f.Priority, f.Active, string(body), f.ConditionLogic, pq.Array(f.ActionLabels), f.ActionFolder, f.ActionStar, f.ActionMarkRead, f.ActionArchive, f.ActionTrash, f.Kind}
	var row *sql.Row
	if id == "" {
		if f.Kind == FilterKindBlockedSender {
			// Blocking the same sender twice is idempotent: return the existing rule.
			existing, err := scanInboxFilter(tx.QueryRowContext(ctx, `SELECT `+filterColumns+` FROM inbox_filters WHERE user_id=$1 AND kind='blocked_sender' AND conditions->0->>'value'=$2`, userID, f.Conditions[0].Value))
			if err == nil {
				return &existing, nil
			}
			if err != sql.ErrNoRows {
				return nil, err
			}
		}
		row = tx.QueryRowContext(ctx, `INSERT INTO inbox_filters(user_id,org_id,identity_id,name,priority,active,conditions,condition_logic,action_labels,action_folder,action_star,action_mark_read,action_archive,action_trash,kind,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''),$11,$12,$13,$14,$15,NOW()) RETURNING `+filterColumns, args...)
	} else {
		if _, err = uuid.Parse(id); err != nil {
			return nil, ErrInvalidMailboxInput
		}
		args = append(args, id)
		// kind is immutable; the WHERE clause keeps a stale caller from converting a rule.
		row = tx.QueryRowContext(ctx, `UPDATE inbox_filters SET identity_id=$3,name=$4,priority=$5,active=$6,conditions=$7,condition_logic=$8,action_labels=$9,action_folder=NULLIF($10,''),action_star=$11,action_mark_read=$12,action_archive=$13,action_trash=$14,updated_at=NOW() WHERE user_id=$1 AND org_id=$2 AND kind=$15 AND uuid=$16 RETURNING `+filterColumns, args...)
	}
	saved, err := scanInboxFilter(row)
	if err == sql.ErrNoRows {
		return nil, ErrMailboxNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return &saved, nil
}
func (s *InboxService) DeleteFilter(ctx context.Context, userID int64, id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return ErrInvalidMailboxInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = lockMailboxLabels(ctx, tx, userID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM inbox_filters WHERE uuid=$1 AND user_id=$2`, id, userID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err == nil && n == 0 {
		return ErrMailboxNotFound
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

const (
	FilterKindFilter        = "filter"
	FilterKindBlockedSender = "blocked_sender"
)

var blockedDomainPattern = regexp.MustCompile(`^@[a-z0-9.-]+\.[a-z]{2,}$`)

// normalizeBlockedSender enforces the narrow shape of a blocked-sender rule:
// one "from" condition (exact address or @domain) that sends mail to Spam or
// Trash. Values are lowercased so the unique index makes blocking idempotent.
func normalizeBlockedSender(f *model.InboxFilter) error {
	invalid := func(msg string) error { return fmt.Errorf("%w: %s", ErrInvalidMailboxInput, msg) }
	if len(f.Conditions) != 1 || f.Conditions[0].Field != "from" {
		return invalid("a blocked sender needs exactly one from condition")
	}
	c := &f.Conditions[0]
	c.Value = strings.ToLower(strings.TrimSpace(c.Value))
	switch c.Operator {
	case "equals":
		addr, err := mail.ParseAddress(c.Value)
		if err != nil || addr.Name != "" || addr.Address != c.Value {
			return invalid("enter a valid email address")
		}
	case "endsWith":
		if !blockedDomainPattern.MatchString(c.Value) {
			return invalid("enter a domain as @example.com")
		}
	default:
		return invalid("a blocked sender uses equals (address) or endsWith (@domain)")
	}
	if f.ActionFolder != "spam" && f.ActionFolder != "trash" {
		return invalid("blocked mail goes to spam or trash")
	}
	if len(f.ActionLabels) > 0 || f.ActionStar || f.ActionArchive || f.ActionTrash || f.ActionForward != "" {
		return invalid("blocked senders only support a destination folder and mark read")
	}
	f.ConditionLogic = "all"
	f.Priority = 0
	f.IdentityID = nil
	if strings.TrimSpace(f.Name) == "" {
		f.Name = "Blocked: " + c.Value
	}
	return nil
}

type MailboxChange struct {
	Cursor      string    `json:"cursor"`
	MessageUUID string    `json:"messageUuid"`
	IdentityID  int64     `json:"identityId"`
	DomainID    int64     `json:"domainId"`
	Operation   string    `json:"operation"`
	ChangedAt   time.Time `json:"changedAt"`
}
type InboxFilterTestInput struct {
	From           string   `json:"from"`
	To             []string `json:"to"`
	Subject        string   `json:"subject"`
	Body           string   `json:"body"`
	HasAttachments bool     `json:"hasAttachments"`
}

func TestInboxFilter(f *model.InboxFilter, input InboxFilterTestInput) map[string]interface{} {
	sample := model.ReceivedEmail{FromEmail: input.From, ToEmails: input.To, Subject: input.Subject, TextBody: input.Body, HasAttachments: input.HasAttachments}
	matched := (&ReceivingService{}).matchesFilter(sample, f.Conditions, f.ConditionLogic)
	return map[string]interface{}{"matches": matched, "active": f.Active, "actions": map[string]interface{}{"labels": f.ActionLabels, "folder": f.ActionFolder, "star": f.ActionStar, "markRead": f.ActionMarkRead, "archive": f.ActionArchive, "trash": f.ActionTrash}}
}

type MailboxChanges struct {
	Changes    []MailboxChange `json:"changes"`
	NextCursor string          `json:"nextCursor"`
	HasMore    bool            `json:"hasMore"`
}

func (s *InboxService) Changes(ctx context.Context, userID int64, cursor string, limit int) (*MailboxChanges, error) {
	if cursor == "now" {
		var current int64
		err := s.db.QueryRowContext(ctx, `SELECT cursor FROM mailbox_change_counters WHERE user_id=$1`, userID).Scan(&current)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		return &MailboxChanges{Changes: []MailboxChange{}, NextCursor: strconv.FormatInt(current, 10)}, nil
	}
	if cursor == "" {
		cursor = "0"
	}
	after, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || after < 0 {
		return nil, ErrInvalidMailboxInput
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		return nil, ErrInvalidMailboxInput
	}
	// A repeatable snapshot binds the retention boundary and returned rows.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var oldest, current int64
	err = tx.QueryRowContext(ctx, `SELECT retained_after,cursor FROM mailbox_change_counters WHERE user_id=$1`, userID).Scan(&oldest, &current)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if after < oldest {
		return nil, ErrMailboxCursorExpired
	}
	if after > current {
		return nil, fmt.Errorf("%w: cursor is ahead of this mailbox", ErrInvalidMailboxInput)
	}
	rows, err := tx.QueryContext(ctx, `SELECT cursor,message_uuid,identity_id,domain_id,operation,changed_at FROM mailbox_changes WHERE user_id=$1 AND cursor>$2 ORDER BY cursor LIMIT $3`, userID, after, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &MailboxChanges{Changes: []MailboxChange{}, NextCursor: cursor}
	for rows.Next() {
		var c MailboxChange
		if err = rows.Scan(&c.Cursor, &c.MessageUUID, &c.IdentityID, &c.DomainID, &c.Operation, &c.ChangedAt); err != nil {
			return nil, err
		}
		if len(out.Changes) == limit {
			out.HasMore = true
			break
		}
		out.Changes = append(out.Changes, c)
		out.NextCursor = c.Cursor
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// PruneMailboxChanges keeps a ninety-day recovery window. It records the last
// removed cursor so an offline client gets 410 instead of silently missing work.
func (s *InboxService) PruneMailboxChanges(ctx context.Context, before time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT user_id FROM mailbox_changes WHERE changed_at<$1 ORDER BY user_id`, before)
	if err != nil {
		return err
	}
	users := []int64{}
	for rows.Next() {
		var user int64
		if err = rows.Scan(&user); err != nil {
			rows.Close()
			return err
		}
		users = append(users, user)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, user := range users {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `SELECT cursor FROM mailbox_change_counters WHERE user_id=$1 FOR UPDATE`, user)
		if err == nil {
			_, err = tx.ExecContext(ctx, `WITH removed AS (DELETE FROM mailbox_changes WHERE user_id=$1 AND cursor <= (SELECT COALESCE(max(cursor),0) FROM mailbox_changes WHERE user_id=$1 AND changed_at<$2) RETURNING cursor) UPDATE mailbox_change_counters SET retained_after=GREATEST(retained_after,COALESCE((SELECT max(cursor) FROM removed),0)) WHERE user_id=$1`, user, before)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
func (s *InboxService) RunChangeRetention(ctx context.Context) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cleanup, cancel := context.WithTimeout(ctx, time.Minute)
			_ = s.PruneMailboxChanges(cleanup, time.Now().Add(-90*24*time.Hour))
			cancel()
		}
	}
}

// Label names are embedded in mail/filter arrays. Serialize rename/delete with
// validation and assignment so a successful request cannot leave a stale name.
func lockMailboxLabels(ctx context.Context, tx *sql.Tx, userID int64) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('mailat:labels:'||$1::text,0))`, userID)
	return err
}
