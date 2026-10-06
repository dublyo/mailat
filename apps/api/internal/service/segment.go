package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/provider"
)

// Dynamic list (segment) rules:
//
//	{"match":"all|any","conditions":[{"field":"...","op":"...","value":...}]}
//
// Fields and ops are whitelisted and every value is a bound parameter, so no
// client text ever reaches the SQL string. Predicates use the contact alias c.

const (
	maxSegmentConditions = 20
	maxSegmentRulesBytes = 16 << 10
	maxSegmentTextValue  = 255
	maxSegmentWithinDays = 3650
)

// SegmentError is a rejected segment definition. It unwraps to a
// MailValidationError so callers that map those to 400 need no extra case.
type SegmentError struct{ *provider.MailValidationError }

func (e *SegmentError) Unwrap() error { return e.MailValidationError }

func segmentErr(format string, args ...any) error {
	return &SegmentError{&provider.MailValidationError{Message: "invalid segment: " + fmt.Sprintf(format, args...)}}
}

// IsSegmentError reports whether err is a rejected segment definition.
func IsSegmentError(err error) bool {
	var se *SegmentError
	return errors.As(err, &se)
}

type segmentFieldType int

const (
	segText segmentFieldType = iota
	segNumber
	segDate
	segList
	segAttribute // typed by the value: text, number or bool
)

var segmentFields = map[string]segmentFieldType{
	"email":            segText,
	"first_name":       segText,
	"last_name":        segText,
	"created_at":       segDate,
	"last_engaged_at":  segDate,
	"engagement_score": segNumber,
	"list":             segList,
}

var segmentOps = map[segmentFieldType]map[string]bool{
	segText:   {"eq": true, "neq": true, "contains": true, "starts_with": true, "ends_with": true, "exists": true, "not_exists": true},
	segNumber: {"eq": true, "neq": true, "gt": true, "gte": true, "lt": true, "lte": true},
	segDate:   {"before": true, "after": true, "within_days": true},
	segList:   {"in_list": true, "not_in_list": true},
}

var segmentAttrKey = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

var segmentCmp = map[string]string{"eq": "=", "neq": "<>", "gt": ">", "gte": ">=", "lt": "<", "lte": "<="}

type segmentRules struct {
	Match      string             `json:"match"`
	Conditions []segmentCondition `json:"conditions"`
}

type segmentCondition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
}

// segment is a validated rule set with list references resolved to org list IDs.
type segment struct {
	any   bool
	conds []segmentCond
}

type segmentCond struct {
	column string // contacts column, or "" for attribute/list conditions
	attr   string // attributes key
	op     string
	kind   segmentFieldType
	text   string
	num    float64
	isNum  bool
	boolv  bool
	isBool bool
	when   time.Time
	days   int
	listID int64
}

// sqlArgs allocates positional placeholders as values are bound.
type sqlArgs []any

func (a *sqlArgs) add(v any) string {
	*a = append(*a, v)
	return "$" + strconv.Itoa(len(*a))
}

// ValidateSegmentRules checks a dynamic list definition, including that every
// referenced list is a static list in the same org.
func ValidateSegmentRules(ctx context.Context, db eventoutbox.DBTX, orgID int64, raw []byte) error {
	_, err := parseSegment(ctx, db, orgID, raw)
	return err
}

// buildSegmentPredicate returns a boolean SQL expression over contacts c whose
// placeholders start at $argStart, plus the values to bind to them.
func buildSegmentPredicate(ctx context.Context, db eventoutbox.DBTX, orgID int64, raw []byte, argStart int) (string, []any, error) {
	seg, err := parseSegment(ctx, db, orgID, raw)
	if err != nil {
		return "", nil, err
	}
	args := make(sqlArgs, argStart-1)
	pred := seg.predicate(&args)
	return pred, args[argStart-1:], nil
}

func parseSegment(ctx context.Context, db eventoutbox.DBTX, orgID int64, raw []byte) (*segment, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, segmentErr("rules are required for a dynamic list")
	}
	if len(raw) > maxSegmentRulesBytes {
		return nil, segmentErr("rules are too large")
	}
	var rules segmentRules
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rules); err != nil {
		return nil, segmentErr("rules must be {\"match\":\"all|any\",\"conditions\":[...]}")
	}
	seg := &segment{}
	switch rules.Match {
	case "all", "":
	case "any":
		seg.any = true
	default:
		return nil, segmentErr("match must be all or any")
	}
	if len(rules.Conditions) == 0 || len(rules.Conditions) > maxSegmentConditions {
		return nil, segmentErr("between 1 and %d conditions are required", maxSegmentConditions)
	}
	for i, rc := range rules.Conditions {
		c, err := parseSegmentCondition(ctx, db, orgID, rc)
		if err != nil {
			var se *SegmentError
			if errors.As(err, &se) {
				return nil, segmentErr("condition %d: %s", i+1, strings.TrimPrefix(se.Message, "invalid segment: "))
			}
			return nil, err
		}
		seg.conds = append(seg.conds, c)
	}
	return seg, nil
}

func parseSegmentCondition(ctx context.Context, db eventoutbox.DBTX, orgID int64, rc segmentCondition) (segmentCond, error) {
	c := segmentCond{op: rc.Op}
	if key, ok := strings.CutPrefix(rc.Field, "attributes."); ok {
		if !segmentAttrKey.MatchString(key) {
			return c, segmentErr("attribute keys must match [A-Za-z0-9_]{1,64}")
		}
		c.kind, c.attr = segAttribute, key
		return c, parseAttributeValue(&c, rc.Value)
	}
	kind, ok := segmentFields[rc.Field]
	if !ok {
		return c, segmentErr("unknown field %q", rc.Field)
	}
	c.kind = kind
	if kind != segList {
		c.column = rc.Field
	}
	if !segmentOps[kind][rc.Op] {
		return c, segmentErr("op %q is not valid for field %q", rc.Op, rc.Field)
	}
	switch kind {
	case segText:
		if rc.Op == "exists" || rc.Op == "not_exists" {
			return c, nil
		}
		return c, decodeSegmentText(&c, rc.Value)
	case segNumber:
		return c, decodeSegmentNumber(&c, rc.Value)
	case segDate:
		if rc.Op == "within_days" {
			var d int
			if err := json.Unmarshal(rc.Value, &d); err != nil || d < 1 || d > maxSegmentWithinDays {
				return c, segmentErr("within_days needs a whole number of days between 1 and %d", maxSegmentWithinDays)
			}
			c.days = d
			return c, nil
		}
		var s string
		if err := json.Unmarshal(rc.Value, &s); err != nil {
			return c, segmentErr("date value must be an RFC 3339 timestamp or YYYY-MM-DD")
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			if t, err = time.Parse("2006-01-02", s); err != nil {
				return c, segmentErr("date value must be an RFC 3339 timestamp or YYYY-MM-DD")
			}
		}
		c.when = t
		return c, nil
	default: // segList
		var id string
		if err := json.Unmarshal(rc.Value, &id); err != nil || id == "" || len(id) > 64 {
			return c, segmentErr("list value must be a list UUID")
		}
		var listType string
		err := db.QueryRowContext(ctx, `SELECT id, type FROM lists WHERE org_id=$1 AND uuid::text=lower($2)`, orgID, id).Scan(&c.listID, &listType)
		if err == sql.ErrNoRows {
			return c, segmentErr("list %q not found", id)
		}
		if err != nil {
			return c, fmt.Errorf("failed to resolve segment list: %w", err)
		}
		if listType == "dynamic" {
			return c, segmentErr("list conditions must reference a static list")
		}
		return c, nil
	}
}

// parseAttributeValue types an attribute condition by its op and JSON value.
func parseAttributeValue(c *segmentCond, raw json.RawMessage) error {
	switch c.op {
	case "exists", "not_exists":
		return nil
	case "contains", "starts_with", "ends_with":
		return decodeSegmentText(c, raw)
	case "gt", "gte", "lt", "lte":
		return decodeSegmentNumber(c, raw)
	case "eq", "neq":
		trimmed := bytes.TrimSpace(raw)
		switch {
		case len(trimmed) > 0 && trimmed[0] == '"':
			return decodeSegmentText(c, raw)
		case bytes.Equal(trimmed, []byte("true")) || bytes.Equal(trimmed, []byte("false")):
			c.isBool, c.boolv = true, trimmed[0] == 't'
			return nil
		default:
			return decodeSegmentNumber(c, raw)
		}
	}
	return segmentErr("op %q is not valid for attributes", c.op)
}

func decodeSegmentText(c *segmentCond, raw json.RawMessage) error {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s == "" || utf8.RuneCountInString(s) > maxSegmentTextValue {
		return segmentErr("text value must be 1-%d characters", maxSegmentTextValue)
	}
	c.text = s
	return nil
}

func decodeSegmentNumber(c *segmentCond, raw json.RawMessage) error {
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return segmentErr("number value required")
	}
	c.num, c.isNum = f, true
	return nil
}

func (s *segment) predicate(args *sqlArgs) string {
	parts := make([]string, len(s.conds))
	for i, c := range s.conds {
		parts[i] = "(" + c.sql(args) + ")"
	}
	join := " AND "
	if s.any {
		join = " OR "
	}
	return "(" + strings.Join(parts, join) + ")"
}

func (c segmentCond) sql(args *sqlArgs) string {
	if c.kind == segList {
		member := "EXISTS(SELECT 1 FROM list_contacts lc JOIN lists l ON l.id=lc.list_id WHERE lc.contact_id=c.id AND lc.list_id=" +
			args.add(c.listID) + " AND l.org_id=c.org_id)"
		if c.op == "not_in_list" {
			return "NOT " + member
		}
		return member
	}
	if c.kind == segDate {
		col := "c." + c.column
		switch c.op {
		case "before":
			return col + " < " + args.add(c.when) + "::timestamptz"
		case "after":
			return col + " > " + args.add(c.when) + "::timestamptz"
		default:
			return col + " >= now() - make_interval(days => " + args.add(c.days) + "::int)"
		}
	}
	textExpr, numExpr := "c."+c.column, "c."+c.column
	if c.attr != "" {
		key := args.add(c.attr) + "::text"
		textExpr = "c.attributes->>" + key
		numExpr = "(CASE WHEN jsonb_typeof(c.attributes->" + key + ")='number' THEN (c.attributes->>" + key + ")::double precision END)"
		if c.isBool {
			eq := "c.attributes->" + key + " = to_jsonb(" + args.add(c.boolv) + "::boolean)"
			if c.op == "neq" {
				return "NOT COALESCE(" + eq + ", false)"
			}
			return eq
		}
	}
	text := "lower(COALESCE(" + textExpr + ",''))"
	switch c.op {
	case "exists":
		return "COALESCE(" + textExpr + ",'') <> ''"
	case "not_exists":
		return "COALESCE(" + textExpr + ",'') = ''"
	case "contains":
		return text + " LIKE " + args.add(likePattern(strings.ToLower(c.text))) + " ESCAPE '\\'"
	case "starts_with":
		return text + " LIKE " + args.add(strings.TrimPrefix(likePattern(strings.ToLower(c.text)), "%")) + " ESCAPE '\\'"
	case "ends_with":
		return text + " LIKE " + args.add(strings.TrimSuffix(likePattern(strings.ToLower(c.text)), "%")) + " ESCAPE '\\'"
	}
	if c.isNum {
		if c.op == "neq" {
			return numExpr + " IS DISTINCT FROM " + args.add(c.num) + "::double precision"
		}
		return numExpr + " " + segmentCmp[c.op] + " " + args.add(c.num) + "::double precision"
	}
	// text eq / neq
	return text + " " + segmentCmp[c.op] + " lower(" + args.add(c.text) + "::text)"
}
