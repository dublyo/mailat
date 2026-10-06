package service

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
)

// publishTriggerAutomation makes an active automation in org with a published
// version using triggerType, so the 014 row triggers record events.
func publishTriggerAutomation(t *testing.T, db *sql.DB, org int, triggerType string) int {
	t.Helper()
	var id int
	if err := db.QueryRow(`INSERT INTO automations(org_id,name,trigger_type,updated_at) VALUES($1,$2,$2,now()) RETURNING id`, org, triggerType).Scan(&id); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `WITH v AS (INSERT INTO automation_versions(automation_id,version,trigger_type,workflow,graph_hash) VALUES($1,1,$2,'{}','h') RETURNING id)
		UPDATE automations SET status='active', published_version_id=(SELECT id FROM v) WHERE id=$1`, id, triggerType)
	return id
}

// triggerEvents lists "type/source/listID" for a contact, oldest first.
func triggerEvents(t *testing.T, db *sql.DB, email string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT e.event_type||'/'||e.source||'/'||COALESCE(e.list_id,0) FROM automation_trigger_events e
		JOIN contacts c ON c.id=e.contact_id WHERE lower(c.email)=lower($1) ORDER BY e.id`, email)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func wantEvents(t *testing.T, db *sql.DB, email string, want ...string) {
	t.Helper()
	if got := strings.Join(triggerEvents(t, db, email), ","); got != strings.Join(want, ",") {
		t.Fatalf("%s events: got %q, want %q", email, got, strings.Join(want, ","))
	}
}

func TestAutomationTriggerSourcesPerInsertPath(t *testing.T) {
	f := newContactFixture(t)
	ctx := context.Background()
	db := f.db
	var l1, l2 string
	if err := db.QueryRow(`SELECT (SELECT uuid FROM lists WHERE id=1), (SELECT uuid FROM lists WHERE id=2)`).Scan(&l1, &l2); err != nil {
		t.Fatal(err)
	}

	// Without an active published automation nothing is recorded.
	if _, err := f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: "early@example.test", ListIDs: []int{1}}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "early@example.test")

	publishTriggerAutomation(t, db, 1, AutomationTriggerCreated)
	subscribed := publishTriggerAutomation(t, db, 1, AutomationTriggerSubscribed)

	// API create.
	c, err := f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: "api@example.test", ListIDs: []int{1}})
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "api@example.test", "contact.created/api/0", "contact.subscribed/api/1")

	// API list add; a duplicate membership records nothing.
	if err := f.lists.AddContactsToList(ctx, 1, l2, []string{c.UUID}); err != nil {
		t.Fatal(err)
	}
	if err := f.lists.AddContactsToList(ctx, 1, l2, []string{c.UUID}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "api@example.test", "contact.created/api/0", "contact.subscribed/api/1", "contact.subscribed/api/2")

	// Bulk and list imports.
	if _, err := f.contacts.ImportContacts(ctx, 1, &model.ImportContactsRequest{ListIDs: []int{1}, Contacts: []model.ImportContactRow{{Email: "bulk@example.test"}}}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "bulk@example.test", "contact.created/import/0", "contact.subscribed/import/1")
	if _, err := f.lists.ImportContactsToList(ctx, 1, l2, &model.ImportContactsToListRequest{Contacts: []model.ImportContactRow{{Email: "listimport@example.test"}, {Email: "bulk@example.test"}}}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "listimport@example.test", "contact.created/import/0", "contact.subscribed/import/2")
	wantEvents(t, db, "bulk@example.test", "contact.created/import/0", "contact.subscribed/import/1", "contact.subscribed/import/2")

	// Manual add from the list page.
	if _, err := f.lists.ManualAddContactToList(ctx, 1, l1, &model.ManualAddContactToListRequest{Email: "manual@example.test"}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "manual@example.test", "contact.created/manual/0", "contact.subscribed/manual/1")

	// Preference center joins.
	var manualID int64
	if err := db.QueryRow(`SELECT id FROM contacts WHERE email='manual@example.test'`).Scan(&manualID); err != nil {
		t.Fatal(err)
	}
	token := f.compliance.encodeUnsubscribeData(UnsubscribeData{ContactID: manualID, OrgID: 1})
	if err := f.compliance.UpdatePreferences(ctx, token, []int{1, 2}, "198.51.100.7", "ua"); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "manual@example.test", "contact.created/manual/0", "contact.subscribed/manual/1", "contact.subscribed/preference_center/2")

	// Paths that set no source, or an unlisted one, record unknown.
	mustExec(t, db, `INSERT INTO contacts(id,org_id,email,status,updated_at) VALUES(50,1,'raw@example.test','active',now())`)
	mustExec(t, db, `INSERT INTO list_contacts(list_id,contact_id) VALUES(1,50); INSERT INTO list_contacts(list_id,contact_id,source) VALUES(2,50,'zapier')`)
	wantEvents(t, db, "raw@example.test", "contact.created/unknown/0", "contact.subscribed/unknown/1", "contact.subscribed/unknown/2")

	// A list from another org records nothing.
	mustExec(t, db, `INSERT INTO list_contacts(list_id,contact_id,source) VALUES(3,50,'api')`)
	wantEvents(t, db, "raw@example.test", "contact.created/unknown/0", "contact.subscribed/unknown/1", "contact.subscribed/unknown/2")

	// While paused, its trigger type records nothing.
	mustExec(t, db, `UPDATE automations SET status='paused' WHERE id=$1`, subscribed)
	if _, err := f.contacts.CreateContact(ctx, 1, &model.CreateContactRequest{Email: "paused@example.test", ListIDs: []int{1}}); err != nil {
		t.Fatal(err)
	}
	wantEvents(t, db, "paused@example.test", "contact.created/api/0")

	// Every membership row written by a service path carries its source.
	count(t, db, 0, `SELECT count(*) FROM list_contacts lc JOIN contacts c ON c.id=lc.contact_id WHERE c.id<>50 AND lc.source IS NULL AND c.email<>'early@example.test'`)
}

func TestAutomationTriggerSourceSignupForm(t *testing.T) {
	db, s, _, list, f, _ := signupFixture(t)
	publishTriggerAutomation(t, db, 1, AutomationTriggerCreated)
	publishTriggerAutomation(t, db, 1, AutomationTriggerSubscribed)
	if err := submitSignup(t, s, f, "form@reader.test"); err != nil {
		t.Fatal(err)
	}
	var listID int
	if err := db.QueryRow(`SELECT id FROM lists WHERE uuid=$1`, list.UUID).Scan(&listID); err != nil {
		t.Fatal(err)
	}
	events := triggerEvents(t, db, "form@reader.test")
	if len(events) != 2 || events[0] != "contact.created/signup_form/0" || !strings.HasPrefix(events[1], "contact.subscribed/signup_form/") {
		t.Fatalf("signup events: %v", events)
	}
	var source string
	if err := db.QueryRow(`SELECT lc.source FROM list_contacts lc JOIN contacts c ON c.id=lc.contact_id WHERE c.email='form@reader.test' AND lc.list_id=$1`, listID).Scan(&source); err != nil || source != "signup_form" {
		t.Fatal(source, err)
	}
}
