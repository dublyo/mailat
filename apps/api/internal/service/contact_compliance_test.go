package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/testutil"
	"github.com/dublyo/mailat/api/internal/worker"
)

func TestSuppressedSQLMatchesWorkerCopy(t *testing.T) {
	if suppressedSQL("$1", "c.email") != worker.SuppressedSQL("$1", "c.email") {
		t.Fatal("service and worker suppression predicates diverged")
	}
}

func TestLikePatternAndSortColumns(t *testing.T) {
	if got := likePattern(`50%_a\b`); got != `%50\%\_a\\b%` {
		t.Fatalf("likePattern=%q", got)
	}
	for key, col := range map[string]string{"createdAt": "created_at", "updatedAt": "updated_at", "firstName": "first_name", "lastName": "last_name", "email": "email"} {
		if contactSortColumns[key] != col {
			t.Fatalf("sort %s -> %q", key, contactSortColumns[key])
		}
	}
	if _, ok := contactSortColumns["id; DROP TABLE contacts"]; ok {
		t.Fatal("unexpected sort key")
	}
}

type contactFixture struct {
	db         *sql.DB
	contacts   *ContactService
	lists      *ListService
	compliance *ComplianceService
	l1, l2, l3 int // l3 belongs to org 2
}

func newContactFixture(t *testing.T) *contactFixture {
	t.Helper()
	db := testutil.Database(t)
	cfg := &config.Config{JWTSecret: "contact-test-secret-contact-test-secret"}
	f := &contactFixture{db: db, contacts: NewContactService(db, cfg), lists: NewListService(db, cfg), compliance: NewComplianceService(db, cfg)}
	mustExec(t, db, `INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'One','one',now()),(2,'Two','two',now());
		INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(1,1,'owner@one.test','x',now());
		INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'News',now()),(2,1,'Offers',now()),(3,2,'Foreign',now());
		ALTER TABLE contacts ALTER COLUMN first_name SET DEFAULT '', ALTER COLUMN last_name SET DEFAULT '', ALTER COLUMN consent_source SET DEFAULT '';`)
	f.l1, f.l2, f.l3 = 1, 2, 3
	return f
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB, want int, q string, args ...any) {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil || n != want {
		t.Fatalf("count=%d want=%d err=%v: %s", n, want, err, q)
	}
}

func wantCode(t *testing.T, err error, target *ContactError) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("got %v, want %s", err, target.Code)
	}
}

func TestSuppressionHashParity(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,' Mixed.Case@Example.TEST ','unsubscribe','test')`)
	var stored string
	if err := f.db.QueryRow(`SELECT email_sha256 FROM suppressions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != emailSHA256("mixed.case@example.test") || stored != emailSHA256(" MIXED.case@example.test") {
		t.Fatal("Go and SQL suppression hashes differ")
	}
	for _, addr := range []string{"mixed.case@example.test", "MIXED.CASE@EXAMPLE.TEST"} {
		if ok, err := isSuppressedTx(ctx, f.db, 1, addr); err != nil || !ok {
			t.Fatalf("%s not suppressed: %v", addr, err)
		}
	}
	if ok, _ := isSuppressedTx(ctx, f.db, 2, "mixed.case@example.test"); ok {
		t.Fatal("suppression leaked across orgs")
	}
}

func TestPreferenceCenterUpdate(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `INSERT INTO contacts(id,org_id,email,status,updated_at) VALUES(10,1,'Reader@Example.test','active',now()); INSERT INTO list_contacts(list_id,contact_id) VALUES(1,10)`)
	token := f.compliance.encodeUnsubscribeData(UnsubscribeData{ContactID: 10, OrgID: 1})
	ip := "198.51.100.7"

	wantCode(t, f.compliance.UpdatePreferences(ctx, token, []int{f.l1, f.l3}, ip, "ua"), ErrUnknownList)
	count(t, f.db, 1, `SELECT count(*) FROM list_contacts`)
	count(t, f.db, 0, `SELECT count(*) FROM consent_audit`)

	// Add Offers, keep News.
	if err := f.compliance.UpdatePreferences(ctx, token, []int{f.l1, f.l2}, ip, "ua"); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM consent_audit WHERE action='subscribe' AND source='preference-center' AND list_id=2 AND ip_address=$1`, ip)
	count(t, f.db, 1, `SELECT contact_count FROM lists WHERE id=2`)
	// Remove News.
	if err := f.compliance.UpdatePreferences(ctx, token, []int{f.l2}, ip, "ua"); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 0, `SELECT count(*) FROM list_contacts WHERE list_id=1`)
	count(t, f.db, 1, `SELECT count(*) FROM consent_audit WHERE action='unsubscribe' AND list_id=1 AND ip_address=$1`, ip)

	// A suppressed address (hash match, different case) can't join, but can leave.
	mustExec(t, f.db, `INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'READER@example.TEST','complaint','test')`)
	wantCode(t, f.compliance.UpdatePreferences(ctx, token, []int{f.l1, f.l2}, ip, "ua"), ErrReactivationBlocked)
	mustExec(t, f.db, `DELETE FROM suppressions`)

	// Empty selection unsubscribes and suppresses; repeating it is idempotent.
	for i := 0; i < 2; i++ {
		if err := f.compliance.UpdatePreferences(ctx, token, nil, ip, "ua"); err != nil {
			t.Fatal(err)
		}
	}
	count(t, f.db, 1, `SELECT count(*) FROM contacts WHERE status='unsubscribed'`)
	count(t, f.db, 1, `SELECT count(*) FROM suppressions WHERE reason='unsubscribe' AND source_type='preference-center'`)
	count(t, f.db, 0, `SELECT count(*) FROM list_contacts`)

	// Unsubscribed contacts are never re-added and never reactivated.
	mustExec(t, f.db, `DELETE FROM suppressions`)
	wantCode(t, f.compliance.UpdatePreferences(ctx, token, []int{f.l1}, ip, "ua"), ErrReactivationBlocked)
	count(t, f.db, 0, `SELECT count(*) FROM list_contacts`)
	count(t, f.db, 0, `SELECT count(*) FROM contacts WHERE status='active'`)

	missing := f.compliance.encodeUnsubscribeData(UnsubscribeData{ContactID: 999, OrgID: 1})
	wantCode(t, f.compliance.UpdatePreferences(ctx, missing, nil, ip, "ua"), ErrContactNotFound)
}

func TestUnsubscribeRecordsConsentInTransaction(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `INSERT INTO contacts(id,org_id,email,status,updated_at) VALUES(10,1,'r@example.test','active',now())`)
	token := f.compliance.encodeUnsubscribeData(UnsubscribeData{ContactID: 10, OrgID: 1, EmailID: 5})
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, token, "203.0.113.9", "mail"); err != nil {
		t.Fatal(err)
	}
	if err := f.compliance.ConfirmUnsubscribe(ctx, token, "too many", "203.0.113.9", "web"); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM suppressions WHERE source_type='email' AND source_id='5'`)
	count(t, f.db, 2, `SELECT count(*) FROM consent_audit WHERE action='unsubscribe' AND ip_address='203.0.113.9'`)
	// Missing contacts are treated as already unsubscribed.
	gone := f.compliance.encodeUnsubscribeData(UnsubscribeData{ContactID: 404, OrgID: 1})
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, gone, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateContactStatusGuard(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `INSERT INTO contacts(id,uuid,org_id,email,status,updated_at) VALUES
		(1,'00000000-0000-0000-0000-000000000001',1,'active@x.test','active',now()),
		(2,'00000000-0000-0000-0000-000000000002',1,'bounced@x.test','bounced',now()),
		(3,'00000000-0000-0000-0000-000000000003',1,'bounced-supp@x.test','bounced',now()),
		(4,'00000000-0000-0000-0000-000000000004',1,'unsub@x.test','unsubscribed',now()),
		(5,'00000000-0000-0000-0000-000000000005',1,'complained@x.test','complained',now()),
		(6,'00000000-0000-0000-0000-000000000006',1,'pending@x.test','pending',now());
		INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'Bounced-Supp@x.test','bounce','ses')`)
	actor := ContactActor{UserID: 1, IP: "192.0.2.44", UA: "admin-ui"}
	uuid := func(n int) string { return "00000000-0000-0000-0000-00000000000" + string(rune('0'+n)) }
	update := func(n int, req model.UpdateContactRequest) error {
		_, err := f.contacts.UpdateContact(ctx, 1, actor, uuid(n), &req)
		return err
	}

	if err := update(1, model.UpdateContactRequest{Status: "active"}); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 0, `SELECT count(*) FROM consent_audit`)
	if err := update(2, model.UpdateContactRequest{Status: "active"}); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM consent_audit WHERE contact_id=2 AND action='status_change' AND source='admin' AND ip_address='192.0.2.44' AND user_agent='admin-ui' AND details LIKE 'Changed by user 1%'`)
	for _, n := range []int{3, 4, 5, 6} {
		wantCode(t, update(n, model.UpdateContactRequest{Status: "active"}), ErrReactivationBlocked)
	}
	for _, s := range []string{"bounced", "complained", "pending", "weird"} {
		wantCode(t, update(1, model.UpdateContactRequest{Status: s}), ErrSystemStatus)
	}
	if err := update(1, model.UpdateContactRequest{Status: "unsubscribed"}); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM contacts WHERE id=1 AND status='unsubscribed'`)
	count(t, f.db, 1, `SELECT count(*) FROM consent_audit WHERE contact_id=1 AND action='unsubscribe' AND source='admin'`)
	count(t, f.db, 1, `SELECT count(*) FROM suppressions WHERE email='active@x.test' AND reason='unsubscribe' AND source_type='admin'`)

	wantCode(t, update(2, model.UpdateContactRequest{Email: "UNSUB@x.test"}), ErrDuplicateEmail)
	var ce *ContactError
	if err := update(2, model.UpdateContactRequest{FirstName: strings.Repeat("a", 101)}); !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("long name: %v", err)
	}
	if err := update(2, model.UpdateContactRequest{Email: " New.Address@X.test "}); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM contacts WHERE id=2 AND email='new.address@x.test'`)
}

func TestCreateAndImportContacts(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()

	_, err := f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: "a@x.test", ListIDs: []int{f.l3}})
	wantCode(t, err, ErrUnknownList)
	count(t, f.db, 0, `SELECT count(*) FROM contacts`)
	count(t, f.db, 0, `SELECT count(*) FROM list_contacts`)

	c, err := f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: " Mixed@X.test ", ListIDs: []int{f.l1, f.l1}})
	if err != nil || c.Email != "mixed@x.test" {
		t.Fatalf("create: %v %+v", err, c)
	}
	count(t, f.db, 1, `SELECT contact_count FROM lists WHERE id=1`)
	_, err = f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: "MIXED@x.test"})
	wantCode(t, err, ErrContactExists)
	mustExec(t, f.db, `INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'Blocked@X.test','unsubscribe','test')`)
	_, err = f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: "blocked@x.test"})
	wantCode(t, err, ErrContactSuppressed)

	_, err = f.contacts.ImportContacts(ctx, 1, &model.ImportContactsRequest{Contacts: []model.ImportContactRow{{Email: "z@x.test"}}, ListIDs: []int{f.l1, f.l3}})
	wantCode(t, err, ErrUnknownList)
	count(t, f.db, 0, `SELECT count(*) FROM contacts WHERE email='z@x.test'`)
	_, err = f.contacts.ImportContacts(ctx, 1, &model.ImportContactsRequest{Contacts: make([]model.ImportContactRow, MaxContactImportRows+1)})
	if ce := (*ContactError)(nil); !errors.As(err, &ce) || ce.Status != 400 {
		t.Fatalf("row cap: %v", err)
	}

	mustExec(t, f.db, `INSERT INTO contacts(org_id,email,status,updated_at) VALUES(1,'gone@x.test','unsubscribed',now())`)
	res, err := f.contacts.ImportContacts(ctx, 1, &model.ImportContactsRequest{
		ListIDs:       []int{f.l2},
		ConsentSource: "webinar-2026",
		Contacts: []model.ImportContactRow{
			{Email: "One@X.test", FirstName: "One"},
			{Email: "two@x.test"},
			{Email: "three@x.test", FirstName: strings.Repeat("n", 101)},
			{Email: "four@x.test"},
			{Email: "five@x.test"},
			{Email: "not-an-email"},
			{Email: "BLOCKED@x.test"},
			{Email: "Gone@x.test"},
			{Email: "one@x.test"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Imported != 4 || res.Skipped != 1 || res.Suppressed != 2 || len(res.Errors) != 2 ||
		!strings.HasPrefix(res.Errors[0], "row 3:") || !strings.HasPrefix(res.Errors[1], "row 6:") {
		t.Fatalf("unexpected import result %+v", res)
	}
	count(t, f.db, 4, `SELECT count(*) FROM list_contacts WHERE list_id=2`)
	count(t, f.db, 4, `SELECT contact_count FROM lists WHERE id=2`)
	count(t, f.db, 0, `SELECT count(*) FROM contacts WHERE lower(email) IN ('blocked@x.test','three@x.test')`)
	count(t, f.db, 0, `SELECT count(*) FROM list_contacts lc JOIN contacts c ON c.id=lc.contact_id WHERE c.email='gone@x.test'`)
	count(t, f.db, 4, `SELECT count(*) FROM consent_audit WHERE action='consent_given' AND source='import' AND details='webinar-2026'`)
	count(t, f.db, 1, `SELECT count(*) FROM contacts WHERE email='one@x.test'`)
}

func TestListImportAndManualAddNormalize(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	var listUUID string
	f.db.QueryRow(`SELECT uuid FROM lists WHERE id=1`).Scan(&listUUID)

	res, err := f.lists.ImportContactsToList(ctx, 1, listUUID, &model.ImportContactsToListRequest{Contacts: []model.ImportContactRow{{Email: "A@X.com"}, {Email: "a@x.com"}, {Email: ""}}})
	if err != nil || res.Imported != 1 || res.Skipped != 2 {
		t.Fatalf("list import %+v %v", res, err)
	}
	c, err := f.lists.ManualAddContactToList(ctx, 1, listUUID, &model.ManualAddContactToListRequest{Email: "A@x.COM"})
	if err != nil || c.Email != "a@x.com" {
		t.Fatalf("manual add %+v %v", c, err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM contacts`)
	count(t, f.db, 1, `SELECT contact_count FROM lists WHERE id=1`)
	count(t, f.db, 1, `SELECT count(*) FROM consent_audit WHERE action='consent_given'`)

	mustExec(t, f.db, `INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'S@x.com','complaint','ses'); INSERT INTO contacts(org_id,email,status,updated_at) VALUES(1,'b@x.com','bounced',now())`)
	_, err = f.lists.ManualAddContactToList(ctx, 1, listUUID, &model.ManualAddContactToListRequest{Email: "s@X.com"})
	wantCode(t, err, ErrContactSuppressed)
	_, err = f.lists.ManualAddContactToList(ctx, 1, listUUID, &model.ManualAddContactToListRequest{Email: "B@x.com"})
	wantCode(t, err, ErrContactSuppressed)
	res, err = f.lists.ImportContactsToList(ctx, 1, listUUID, &model.ImportContactsToListRequest{Contacts: []model.ImportContactRow{{Email: "s@x.com"}, {Email: "b@x.com"}}})
	if err != nil || res.Suppressed != 2 {
		t.Fatalf("suppressed list import %+v %v", res, err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM list_contacts`)
}

func TestListContactsSearchAndSort(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `INSERT INTO contacts(org_id,email,first_name,status,updated_at) VALUES
		(1,'deal@x.test','50% off','active',now()),(1,'fifty@x.test','500 club','active',now()),
		(1,'under_score@x.test','Zed','active',now()),(1,'underXscore@x.test','Amy','active',now())`)
	res, err := f.contacts.ListContacts(ctx, 1, &model.ContactSearchRequest{Query: "50%"})
	if err != nil || res.Total != 1 || res.Contacts[0].Email != "deal@x.test" {
		t.Fatalf("literal %%: %+v %v", res, err)
	}
	res, err = f.contacts.ListContacts(ctx, 1, &model.ContactSearchRequest{Query: "under_"})
	if err != nil || res.Total != 1 {
		t.Fatalf("literal _: %+v %v", res, err)
	}
	res, err = f.contacts.ListContacts(ctx, 1, &model.ContactSearchRequest{SortBy: "firstName", SortOrder: "asc"})
	if err != nil || len(res.Contacts) != 4 {
		t.Fatal(err)
	}
	var names []string
	for _, c := range res.Contacts {
		names = append(names, c.FirstName)
	}
	if strings.Join(names, ",") != "50% off,500 club,Amy,Zed" {
		t.Fatalf("sorted %v", names)
	}
}

func TestGDPRErasureCompleteness(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	addr := "erase.me@example.test"
	mustExec(t, f.db, `
		INSERT INTO domains(id,org_id,name,verification_token,status,updated_at) VALUES(1,1,'one.test','t','active',now());
		INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,1,1,'news@one.test',now());
		INSERT INTO contacts(id,uuid,org_id,email,first_name,status,updated_at) VALUES
			(10,'00000000-0000-0000-0000-0000000000aa',1,'Erase.Me@example.test','Eve','active',now()),
			(11,'00000000-0000-0000-0000-0000000000bb',1,'erase.me@EXAMPLE.test','Eve','active',now()),
			(12,'00000000-0000-0000-0000-0000000000cc',1,'keep@example.test','Kim','active',now());
		INSERT INTO list_contacts(list_id,contact_id) VALUES(1,10),(2,11),(1,12);
		UPDATE lists SET contact_count=(SELECT count(*) FROM list_contacts WHERE list_id=lists.id);
		INSERT INTO consent_audit(contact_id,org_id,action,source,ip_address) VALUES(10,1,'subscribe','form','192.0.2.1'),(11,1,'subscribe','form','192.0.2.1');
		INSERT INTO automations(id,org_id,name,trigger_type,updated_at) VALUES(1,1,'Welcome','contact_created',now());
		INSERT INTO automation_enrollments(id,automation_id,contact_id,org_id,updated_at) VALUES(1,1,10,1,now()),(2,1,12,1,now());
		INSERT INTO automation_logs(enrollment_id,automation_id,step_index,step_type,message) VALUES(1,1,0,'email','sent to Erase.Me@example.test'),(2,1,0,'email','kept');
		INSERT INTO emails(id,org_id,message_id,identity_id,from_email,to_emails,cc_emails,subject,html_content,text_content,domain_id,contact_id,updated_at) VALUES
			(1,1,'m1',1,'news@one.test',ARRAY['Eve <Erase.Me@example.test>'],'{}','Hi Eve','<p>Eve</p>','Eve',1,10,now()),
			(2,1,'m2',1,'news@one.test',ARRAY['keep@example.test'],ARRAY['erase.me@example.test'],'Team note','<p>x</p>','x',1,12,now()),
			(3,1,'m3',1,'news@one.test',ARRAY['keep@example.test'],'{}','Untouched','<p>k</p>','k',1,12,now());
		INSERT INTO delivery_events(email_id,event_type,data) VALUES(1,'opened','{"recipient":"erase.me@example.test"}'),(3,'opened','{"recipient":"keep@example.test"}');
		INSERT INTO webhooks(id,org_id,name,url,secret,updated_at) VALUES(1,1,'Hook','https://hook.test','s',now());
		INSERT INTO webhook_events(id,org_id,event_type,dedupe_key,payload) VALUES
			('10000000-0000-0000-0000-000000000001',1,'contact.created','a','{"email":"ERASE.ME@example.test"}'),
			('10000000-0000-0000-0000-000000000002',1,'contact.updated','b','{"contact_id":"00000000-0000-0000-0000-0000000000bb"}'),
			('10000000-0000-0000-0000-000000000003',1,'contact.created','c','{"email":"keep@example.test"}');
		INSERT INTO webhook_deliveries(id,event_id,webhook_id,status) VALUES
			('20000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000001',1,'pending'),
			('20000000-0000-0000-0000-000000000002','10000000-0000-0000-0000-000000000002',1,'delivered'),
			('20000000-0000-0000-0000-000000000003','10000000-0000-0000-0000-000000000003',1,'pending');
		INSERT INTO webhook_delivery_attempts(delivery_id,attempt,replay,response_body,duration_ms) VALUES
			('20000000-0000-0000-0000-000000000002',1,0,'echo erase.me@example.test',5);
		INSERT INTO webhook_calls(webhook_id,event_type,payload,response_body) VALUES(1,'contact.created','{"email":"Erase.Me@example.test"}','ok erase.me@example.test');
		INSERT INTO signup_forms(id,org_id,list_id,created_by,name,title,consent_text,button_text,updated_at) VALUES(1,1,1,1,'F','T','C','B',now());
		INSERT INTO signup_requests(form_id,email,token_hash,status,confirmation_mode,disclosure,form_version,expires_at) VALUES(1,'Erase.Me@example.test','h','pending','double','d',1,now()+interval '1 day');
		INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'ERASE.ME@example.test','unsubscribe','test');`)

	actor := ContactActor{UserID: 1, IP: "192.0.2.9", UA: "admin"}
	if err := f.compliance.DeleteContactData(ctx, 1, actor, "00000000-0000-0000-0000-0000000000aa"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"contacts", "list_contacts", "consent_audit", "automation_enrollments", "automation_logs", "emails", "delivery_events", "webhook_events", "webhook_delivery_attempts", "webhook_calls", "signup_requests", "suppressions", "audit_logs"} {
		count(t, f.db, 0, `SELECT count(*) FROM `+table+` x WHERE x::text ILIKE '%erase.me%'`)
	}
	count(t, f.db, 0, `SELECT count(*) FROM webhook_events WHERE payload::text LIKE '%0000000000bb%'`)
	count(t, f.db, 1, `SELECT count(*) FROM contacts`)
	count(t, f.db, 0, `SELECT count(*) FROM automation_enrollments WHERE contact_id IN (10,11)`)
	count(t, f.db, 1, `SELECT count(*) FROM automation_logs`)
	count(t, f.db, 2, `SELECT count(*) FROM emails WHERE subject='[redacted]' AND contact_id IS NULL AND html_content IS NULL`)
	count(t, f.db, 1, `SELECT count(*) FROM emails WHERE subject='Untouched' AND contact_id=12`)
	count(t, f.db, 1, `SELECT count(*) FROM webhook_deliveries WHERE status='cancelled'`)
	count(t, f.db, 1, `SELECT count(*) FROM webhook_deliveries WHERE id='20000000-0000-0000-0000-000000000002' AND status='delivered'`)
	count(t, f.db, 1, `SELECT count(*) FROM webhook_deliveries WHERE id='20000000-0000-0000-0000-000000000003' AND status='pending'`)
	count(t, f.db, 1, `SELECT contact_count FROM lists WHERE id=1`)
	count(t, f.db, 0, `SELECT contact_count FROM lists WHERE id=2`)
	h := emailSHA256(addr)
	count(t, f.db, 1, `SELECT count(*) FROM suppressions`)
	count(t, f.db, 1, `SELECT count(*) FROM suppressions WHERE email='erased:'||$1 AND email_sha256=$1 AND reason='gdpr_erasure'`, h)
	count(t, f.db, 1, `SELECT count(*) FROM audit_logs WHERE action='contact_erased' AND new_values->>'emailSha256'=$1 AND user_id=1`, h)

	// The hash-only row keeps blocking: create, import and campaign selection.
	_, err := f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: addr})
	wantCode(t, err, ErrContactSuppressed)
	mustExec(t, f.db, `INSERT INTO contacts(id,org_id,email,status,updated_at) VALUES(20,1,'Erase.Me@example.test','active',now()); INSERT INTO list_contacts(list_id,contact_id) VALUES(1,20)`)
	count(t, f.db, 1, `SELECT count(*) FROM contacts c JOIN list_contacts lc ON lc.contact_id=c.id WHERE lc.list_id=1 AND c.status='active' AND NOT `+suppressedSQL("1", "c.email"))
	wantCode(t, f.compliance.DeleteContactData(ctx, 1, actor, "00000000-0000-0000-0000-0000000000aa"), ErrContactNotFound)
}
