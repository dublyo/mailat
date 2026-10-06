package worker

import (
	"context"
	"errors"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
	"testing"
)

func TestCampaignRejectsNoLongerEligibleSignup(t *testing.T) {
	db := testutil.Database(t)
	_, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'QA','qa',now()); INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'Readers',now()); INSERT INTO contacts(id,org_id,email,first_name,last_name,status,updated_at) VALUES(1,1,'reader@example.test','','','unsubscribed',now()); INSERT INTO list_contacts(list_id,contact_id) VALUES(1,1);`)
	if err != nil {
		t.Fatal(err)
	}
	h := NewCampaignHandler(db, &config.Config{})
	campaign := &campaignInfo{OrgID: 1, ListID: 1}
	contact := contactInfo{ID: 1, Email: "reader@example.test"}
	assertSkipped := func() {
		t.Helper()
		if err := h.sendCampaignEmail(context.Background(), campaign, contact); !errors.Is(err, errCampaignIneligible) {
			t.Fatalf("expected skipped delivery, got %v", err)
		}
	}
	assertSkipped()
	if _, err = db.Exec(`UPDATE contacts SET status='active'; INSERT INTO suppressions(org_id,email,reason,source_type) VALUES(1,'READER@example.test','complaint','test')`); err != nil {
		t.Fatal(err)
	}
	assertSkipped()
	if _, err = db.Exec(`DELETE FROM suppressions; DELETE FROM list_contacts`); err != nil {
		t.Fatal(err)
	}
	assertSkipped()
	var count int
	db.QueryRow(`SELECT count(*) FROM emails`).Scan(&count)
	if count != 0 {
		t.Fatal("ineligible email queued")
	}
}
