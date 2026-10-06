package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
)

func segRaw(match string, conds ...string) []byte {
	return []byte(`{"match":"` + match + `","conditions":[` + strings.Join(conds, ",") + `]}`)
}

func TestSegmentOpsAreParameterised(t *testing.T) {
	const marker = "zz'; DROP TABLE contacts; --"
	cases := []string{
		`{"field":"email","op":"eq","value":%q}`,
		`{"field":"email","op":"neq","value":%q}`,
		`{"field":"first_name","op":"contains","value":%q}`,
		`{"field":"last_name","op":"starts_with","value":%q}`,
		`{"field":"email","op":"ends_with","value":%q}`,
		`{"field":"attributes.plan","op":"eq","value":%q}`,
		`{"field":"attributes.plan","op":"contains","value":%q}`,
	}
	for _, tc := range cases {
		raw := segRaw("all", fmt.Sprintf(tc, marker))
		sql, args, err := buildSegmentPredicate(context.Background(), nil, 1, raw, 3)
		if err != nil {
			t.Fatalf("%s: %v", tc, err)
		}
		if strings.Contains(sql, "DROP") || !strings.Contains(sql, "$3") || len(args) == 0 {
			t.Fatalf("%s: value not bound: %s %v", tc, sql, args)
		}
		if strings.Contains(sql, "$1") || strings.Contains(sql, "$2 ") {
			t.Fatalf("%s: placeholders must start at argStart: %s", tc, sql)
		}
	}
	other := []string{
		`{"field":"email","op":"exists"}`,
		`{"field":"email","op":"not_exists"}`,
		`{"field":"engagement_score","op":"eq","value":1}`,
		`{"field":"engagement_score","op":"neq","value":1}`,
		`{"field":"engagement_score","op":"gt","value":1.5}`,
		`{"field":"engagement_score","op":"gte","value":1}`,
		`{"field":"engagement_score","op":"lt","value":1}`,
		`{"field":"engagement_score","op":"lte","value":1}`,
		`{"field":"created_at","op":"before","value":"2026-01-01"}`,
		`{"field":"last_engaged_at","op":"after","value":"2026-01-01T00:00:00Z"}`,
		`{"field":"last_engaged_at","op":"within_days","value":30}`,
		`{"field":"attributes.vip","op":"eq","value":true}`,
		`{"field":"attributes.age","op":"gte","value":18}`,
		`{"field":"attributes.age","op":"exists"}`,
	}
	_, args, err := buildSegmentPredicate(context.Background(), nil, 1, segRaw("any", other...), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(args) < 12 {
		t.Fatalf("expected bound values, got %d", len(args))
	}
}

func TestSegmentRejectsBadRules(t *testing.T) {
	many := make([]string, 21)
	for i := range many {
		many[i] = `{"field":"email","op":"exists"}`
	}
	bad := map[string][]byte{
		"injection key":   segRaw("all", `{"field":"attributes.a'b","op":"exists"}`),
		"dotted key":      segRaw("all", `{"field":"attributes.a.b","op":"exists"}`),
		"long key":        segRaw("all", `{"field":"attributes.`+strings.Repeat("k", 65)+`","op":"exists"}`),
		"unknown field":   segRaw("all", `{"field":"password_hash","op":"eq","value":"x"}`),
		"unknown op":      segRaw("all", `{"field":"email","op":"like","value":"x"}`),
		"text op on num":  segRaw("all", `{"field":"engagement_score","op":"contains","value":"1"}`),
		"num op on text":  segRaw("all", `{"field":"email","op":"gt","value":1}`),
		"date op on text": segRaw("all", `{"field":"email","op":"before","value":"2026-01-01"}`),
		"num value text":  segRaw("all", `{"field":"engagement_score","op":"eq","value":"1"}`),
		"bad date":        segRaw("all", `{"field":"created_at","op":"before","value":"yesterday"}`),
		"bad days":        segRaw("all", `{"field":"created_at","op":"within_days","value":0}`),
		"date on attr":    segRaw("all", `{"field":"attributes.x","op":"before","value":"2026-01-01"}`),
		"list op on text": segRaw("all", `{"field":"email","op":"in_list","value":"x"}`),
		"empty value":     segRaw("all", `{"field":"email","op":"eq","value":""}`),
		"bad match":       segRaw("most", `{"field":"email","op":"exists"}`),
		"no conditions":   segRaw("all"),
		"too many":        segRaw("all", many...),
		"extra key":       []byte(`{"match":"all","conditions":[{"field":"email","op":"exists"}],"sql":"1=1"}`),
		"not object":      []byte(`[1,2]`),
		"null":            []byte(`null`),
	}
	for name, raw := range bad {
		err := ValidateSegmentRules(context.Background(), nil, 1, raw)
		var mve *provider.MailValidationError
		if !IsSegmentError(err) || !errors.As(err, &mve) {
			t.Errorf("%s: want segment validation error, got %v", name, err)
		}
	}
}

func TestSegmentEvaluatesAgainstContacts(t *testing.T) {
	f := newContactFixture(t)
	mustExec(t, f.db, `INSERT INTO lists(id,org_id,name,type,segment_rules,updated_at) VALUES(4,1,'Dyn','dynamic','{"match":"all","conditions":[{"field":"email","op":"exists"}]}',now());
		INSERT INTO contacts(id,org_id,email,first_name,last_name,attributes,engagement_score,last_engaged_at,created_at,updated_at) VALUES
		(1,1,'ann@a.test','Ann','Lee','{"plan":"Pro","age":30,"vip":true}',10,now()-interval '2 days','2025-01-01',now()),
		(2,1,'bob@b.test','Bob','','{"plan":"free","age":"30"}',2,NULL,'2026-06-01',now()),
		(3,1,'c_d@a.test','','Ng','{}',5,now()-interval '40 days','2026-06-01',now()),
		(4,2,'ann@a.test','Ann','','{"plan":"Pro"}',10,now(),'2025-01-01',now());
		INSERT INTO list_contacts(list_id,contact_id) VALUES(1,2),(3,4);`)
	var l1, l3, l4 string
	f.db.QueryRow(`SELECT uuid FROM lists WHERE id=1`).Scan(&l1)
	f.db.QueryRow(`SELECT uuid FROM lists WHERE id=3`).Scan(&l3)
	f.db.QueryRow(`SELECT uuid FROM lists WHERE id=4`).Scan(&l4)

	match := func(raw []byte) string {
		t.Helper()
		pred, args, err := buildSegmentPredicate(context.Background(), f.db, 1, raw, 2)
		if err != nil {
			t.Fatal(err)
		}
		rows, err := f.db.Query(`SELECT c.id FROM contacts c WHERE c.org_id=$1 AND `+pred+` ORDER BY c.id`, append([]any{1}, args...)...)
		if err != nil {
			t.Fatalf("%s: %v", pred, err)
		}
		defer rows.Close()
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		return strings.Join(ids, ",")
	}
	cases := []struct {
		raw  []byte
		want string
	}{
		{segRaw("all", `{"field":"email","op":"eq","value":"ANN@a.test"}`), "1"},
		{segRaw("all", `{"field":"email","op":"neq","value":"ann@a.test"}`), "2,3"},
		{segRaw("all", `{"field":"email","op":"contains","value":"_"}`), "3"}, // LIKE metachar is literal
		{segRaw("all", `{"field":"email","op":"ends_with","value":"@A.TEST"}`), "1,3"},
		{segRaw("all", `{"field":"first_name","op":"starts_with","value":"b"}`), "2"},
		{segRaw("all", `{"field":"last_name","op":"not_exists"}`), "2"},
		{segRaw("all", `{"field":"engagement_score","op":"gte","value":5}`), "1,3"},
		{segRaw("all", `{"field":"created_at","op":"before","value":"2026-01-01"}`), "1"},
		{segRaw("all", `{"field":"last_engaged_at","op":"within_days","value":7}`), "1"},
		{segRaw("all", `{"field":"last_engaged_at","op":"after","value":"2020-01-01"}`), "1,3"},
		{segRaw("all", `{"field":"attributes.plan","op":"eq","value":"pro"}`), "1"},
		{segRaw("all", `{"field":"attributes.age","op":"gt","value":18}`), "1"}, // "30" string is not a number
		{segRaw("all", `{"field":"attributes.vip","op":"eq","value":true}`), "1"},
		{segRaw("all", `{"field":"attributes.vip","op":"neq","value":true}`), "2,3"},
		{segRaw("all", `{"field":"attributes.plan","op":"exists"}`), "1,2"},
		{segRaw("all", `{"field":"list","op":"in_list","value":"`+l1+`"}`), "2"},
		{segRaw("all", `{"field":"list","op":"not_in_list","value":"`+strings.ToUpper(l1)+`"}`), "1,3"},
		{segRaw("any", `{"field":"first_name","op":"eq","value":"bob"}`, `{"field":"engagement_score","op":"eq","value":5}`), "2,3"},
	}
	for _, tc := range cases {
		if got := match(tc.raw); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.raw, got, tc.want)
		}
	}
	for name, raw := range map[string][]byte{
		"foreign list": segRaw("all", `{"field":"list","op":"in_list","value":"`+l3+`"}`),
		"unknown list": segRaw("all", `{"field":"list","op":"in_list","value":"00000000-0000-0000-0000-000000000000"}`),
		"dynamic list": segRaw("all", `{"field":"list","op":"in_list","value":"`+l4+`"}`),
	} {
		if err := ValidateSegmentRules(context.Background(), f.db, 1, raw); !IsSegmentError(err) {
			t.Errorf("%s: want segment error, got %v", name, err)
		}
	}
}

func TestListServiceValidatesSegmentsAndGuardsDelete(t *testing.T) {
	f := newContactFixture(t)
	mustExec(t, f.db, `SELECT setval(pg_get_serial_sequence('lists','id'), 100)`)
	ctx := context.Background()
	rules := map[string]any{"match": "all", "conditions": []any{map[string]any{"field": "email", "op": "ends_with", "value": "@a.test"}}}
	if _, err := f.lists.CreateList(ctx, 1, &model.CreateListRequest{Name: "Bad", Type: "dynamic"}); !IsSegmentError(err) {
		t.Fatalf("dynamic list without rules: %v", err)
	}
	if _, err := f.lists.CreateList(ctx, 1, &model.CreateListRequest{Name: "Bad", Type: "dynamic", SegmentRules: map[string]any{"match": "all", "conditions": []any{map[string]any{"field": "x", "op": "eq", "value": "y"}}}}); !IsSegmentError(err) {
		t.Fatalf("unknown field: %v", err)
	}
	if _, err := f.lists.CreateList(ctx, 1, &model.CreateListRequest{Name: "Bad", Type: "weird"}); err == nil {
		t.Fatal("unknown list type accepted")
	}
	dyn, err := f.lists.CreateList(ctx, 1, &model.CreateListRequest{Name: "Segment", Type: "dynamic", SegmentRules: rules})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.lists.UpdateList(ctx, 1, dyn.UUID, &model.UpdateListRequest{SegmentRules: map[string]any{"match": "all", "conditions": []any{}}}); !IsSegmentError(err) {
		t.Fatalf("update with empty conditions: %v", err)
	}
	if _, err = f.lists.UpdateList(ctx, 1, dyn.UUID, &model.UpdateListRequest{Name: "Renamed"}); err != nil {
		t.Fatalf("rename without rules: %v", err)
	}

	var staticUUID string
	f.db.QueryRow(`SELECT uuid FROM lists WHERE id=1`).Scan(&staticUUID)
	mustExec(t, f.db, `INSERT INTO campaigns(org_id,name,subject,from_name,from_email,list_id,status,updated_at) VALUES(1,'C','S','F','f@a.test',1,'paused',now())`)
	if err = f.lists.DeleteList(ctx, 1, staticUUID); err == nil || !strings.Contains(err.Error(), "active campaigns") {
		t.Fatalf("delete with paused campaign: %v", err)
	}
	if err = f.lists.DeleteList(ctx, 1, dyn.UUID); err != nil {
		t.Fatalf("delete unused dynamic list: %v", err)
	}
}
