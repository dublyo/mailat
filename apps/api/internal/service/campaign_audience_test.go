package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/dublyo/mailat/api/internal/testutil"
)

const audienceRun = "11111111-1111-1111-1111-111111111111"

// newAudienceFixture: org 1 with static list 1 and campaign 1 on it; org 2 has
// a same-address contact that must never leak in.
func newAudienceFixture(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.Database(t)
	mustExec(t, db, `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'QA','qa',now()),(2,'Other','other',now());
		INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'Readers',now()),(2,2,'Foreign',now());
		INSERT INTO campaigns(id,org_id,name,subject,from_name,from_email,list_id,status,updated_at) VALUES(1,1,'C','S','F','f@qa.test',1,'sending',now());`)
	return db
}

func addContact(t *testing.T, db *sql.DB, id, org int, email, status string, lists ...int) {
	t.Helper()
	mustExec(t, db, `INSERT INTO contacts(id,org_id,email,first_name,last_name,status,updated_at) VALUES($1,$2,$3,'First','Last',$4,now())`, id, org, email, status)
	for _, l := range lists {
		mustExec(t, db, `INSERT INTO list_contacts(list_id,contact_id) VALUES($1,$2)`, l, id)
	}
}

func audienceFor(t *testing.T, db *sql.DB, listID int64) *campaignAudience {
	t.Helper()
	a, err := loadCampaignAudience(context.Background(), db, 1, listID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func materialiseCampaign(t *testing.T, db *sql.DB, a *campaignAudience) int64 {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	n, err := a.materialise(context.Background(), tx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return n
}

// Ported from worker.TestCampaignRejectsNoLongerEligibleSignup: an unsubscribed
// contact, a contact suppressed in another letter case, and a contact removed
// from the list are never materialised and never queued.
func TestCampaignRejectsNoLongerEligibleSignup(t *testing.T) {
	db := newAudienceFixture(t)
	addContact(t, db, 1, 1, "reader@example.test", "unsubscribed", 1)
	a := audienceFor(t, db, 1)
	assertNone := func() {
		t.Helper()
		if n := materialiseCampaign(t, db, a); n != 0 {
			t.Fatalf("ineligible contact materialised (%d rows)", n)
		}
		count(t, db, 0, `SELECT count(*) FROM campaign_recipients`)
	}
	assertNone()
	mustExec(t, db, `UPDATE contacts SET status='active'; INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'READER@example.test','complaint','test')`)
	assertNone()
	mustExec(t, db, `DELETE FROM suppressions; INSERT INTO suppression_list(org_id,email,reason,source) VALUES(1,'Reader@Example.test','bounce','ses')`)
	assertNone()
	mustExec(t, db, `DELETE FROM suppression_list; DELETE FROM list_contacts`)
	assertNone()
	count(t, db, 0, `SELECT count(*) FROM emails`)
}

func TestCampaignAudienceEstimateAndMaterialise(t *testing.T) {
	db := newAudienceFixture(t)
	addContact(t, db, 1, 1, "a@x.test", "active", 1)
	addContact(t, db, 2, 1, "A@X.test", "active", 1) // case variant of 1
	addContact(t, db, 3, 1, "b@x.test", "active", 1)
	addContact(t, db, 4, 1, "pending@x.test", "pending", 1)
	addContact(t, db, 5, 1, "unsub@x.test", "unsubscribed", 1)
	addContact(t, db, 6, 1, "supp@x.test", "active", 1)
	addContact(t, db, 7, 1, "tx@x.test", "active", 1)
	addContact(t, db, 8, 1, "outside@x.test", "active")
	addContact(t, db, 9, 2, "c@x.test", "active", 2)
	mustExec(t, db, `INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'SUPP@x.test','unsubscribe','test'),(2,'b@x.test','unsubscribe','test');
		INSERT INTO suppression_list(org_id,email,reason,source) VALUES(1,'TX@X.TEST','bounce','ses')`)
	a := audienceFor(t, db, 1)
	est, err := a.estimate(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if est.ListType != "static" || est.Eligible != 2 || est.ExcludedInactive != 2 || est.ExcludedSuppressed != 2 {
		t.Fatalf("estimate=%+v", est)
	}
	if n := materialiseCampaign(t, db, a); n != 2 {
		t.Fatalf("materialised %d, want 2", n)
	}
	// The lowest contact id wins a case-duplicate.
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE contact_id=1 AND email='a@x.test' AND org_id=1 AND status='pending'`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE contact_id=3`)
	// Re-running (resume of an unprepared campaign) adds nothing.
	if n := materialiseCampaign(t, db, a); n != 0 {
		t.Fatalf("second materialise inserted %d", n)
	}
}

func TestCampaignAudienceDynamicList(t *testing.T) {
	db := newAudienceFixture(t)
	mustExec(t, db, `INSERT INTO lists(id,org_id,name,type,segment_rules,updated_at) VALUES
		(3,1,'VIP','dynamic','{"match":"all","conditions":[{"field":"email","op":"ends_with","value":"@vip.test"}]}',now()),
		(4,1,'Broken','dynamic','{"match":"all","conditions":[{"field":"nope","op":"eq","value":"x"}]}',now())`)
	addContact(t, db, 1, 1, "one@vip.test", "active")
	addContact(t, db, 2, 1, "TWO@VIP.test", "active")
	addContact(t, db, 3, 1, "three@other.test", "active")
	addContact(t, db, 4, 1, "four@vip.test", "unsubscribed")
	addContact(t, db, 5, 1, "five@vip.test", "active")
	addContact(t, db, 6, 2, "six@vip.test", "active")
	mustExec(t, db, `INSERT INTO suppression_list(org_id,email,reason,source) VALUES(1,'Five@VIP.test','complaint','ses'); UPDATE campaigns SET list_id=3`)
	est, err := estimateAudience(context.Background(), db, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if est.ListType != "dynamic" || est.Eligible != 2 || est.ExcludedInactive != 1 || est.ExcludedSuppressed != 1 {
		t.Fatalf("estimate=%+v", est)
	}
	if n := materialiseCampaign(t, db, audienceFor(t, db, 3)); n != 2 {
		t.Fatalf("materialised %d, want 2", n)
	}
	if _, err = loadCampaignAudience(context.Background(), db, 1, 4); !IsSegmentError(err) {
		t.Fatalf("broken stored rules: %v", err)
	}
	if _, err = loadCampaignAudience(context.Background(), db, 1, 2); err == nil {
		t.Fatal("another org's list resolved")
	}
}

func TestCampaignRecheckSkipsWithReasons(t *testing.T) {
	db := newAudienceFixture(t)
	for i, e := range []string{"ok@x.test", "unsub@x.test", "supp@x.test", "gone@x.test", "moved@x.test", "deleted@x.test", "tx@x.test", "other@x.test"} {
		addContact(t, db, i+1, 1, e, "active", 1)
	}
	a := audienceFor(t, db, 1)
	if n := materialiseCampaign(t, db, a); n != 8 {
		t.Fatalf("materialised %d", n)
	}
	// Row 8 is claimed by another runner and must be left alone.
	mustExec(t, db, `UPDATE campaign_recipients SET status='claimed', lease_owner=CASE WHEN contact_id=8 THEN '22222222-2222-2222-2222-222222222222'::uuid ELSE $1::uuid END, lease_expires_at=now()+interval '5 minutes'`, audienceRun)
	mustExec(t, db, `UPDATE contacts SET status='unsubscribed' WHERE id=2;
		INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'SUPP@X.TEST','unsubscribe','test');
		DELETE FROM list_contacts WHERE contact_id=4;
		UPDATE contacts SET email='new@x.test' WHERE id=5;
		DELETE FROM contacts WHERE id=6;
		INSERT INTO suppression_list(org_id,email,reason,source) VALUES(1,'Tx@x.test','bounce','ses');
		UPDATE contacts SET attributes='{"plan":"pro"}' WHERE id=1;
		UPDATE campaign_recipients SET quota_reserved=true WHERE contact_id IN (1,2,3)`)
	var ids []int64
	rows, err := db.Query(`SELECT id FROM campaign_recipients ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	ok, refund, err := a.recheck(context.Background(), tx, 1, audienceRun, ids)
	if err != nil {
		t.Fatal(err)
	}
	// Reserved rows that are skipped come back for a refund and lose the flag.
	if refund != 2 {
		t.Fatalf("refund=%d want 2", refund)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(ok) != 1 || ok[0].ContactID != 1 || ok[0].Email != "ok@x.test" || ok[0].FirstName != "First" || ok[0].Attributes["plan"] != "pro" || ok[0].MessageUUID == "" {
		t.Fatalf("eligible=%+v", ok)
	}
	for email, reason := range map[string]string{"unsub@x.test": "inactive", "supp@x.test": "suppressed", "gone@x.test": "not_member", "moved@x.test": "email_changed", "deleted@x.test": "contact_deleted", "tx@x.test": "suppressed"} {
		count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE email=$1 AND status='skipped' AND skip_reason=$2 AND lease_owner IS NULL`, email, reason)
	}
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE status='claimed' AND contact_id=1`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE status='claimed' AND contact_id=8`)
	count(t, db, 6, `SELECT skipped_count FROM campaigns WHERE id=1`)
	count(t, db, 1, `SELECT count(*) FROM campaign_recipients WHERE quota_reserved`)

	// A second pass over the same ids changes nothing and keeps the counter.
	ok, refund, err = a.recheck(context.Background(), db, 1, audienceRun, ids)
	if err != nil || len(ok) != 1 || refund != 0 {
		t.Fatalf("second recheck: %v %d %+v", err, refund, ok)
	}
	count(t, db, 6, `SELECT skipped_count FROM campaigns WHERE id=1`)

	// Nothing eligible: the refund still comes back.
	mustExec(t, db, `UPDATE contacts SET status='unsubscribed' WHERE id=1`)
	ok, refund, err = a.recheck(context.Background(), db, 1, audienceRun, ids)
	if err != nil || len(ok) != 0 || refund != 1 {
		t.Fatalf("all skipped: %v %d %+v", err, refund, ok)
	}
	count(t, db, 7, `SELECT skipped_count FROM campaigns WHERE id=1`)
}
