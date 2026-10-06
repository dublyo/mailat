package service

import (
	"context"
	"testing"
)

func campaignRecipientFixture(t *testing.T) *contactFixture {
	t.Helper()
	f := newContactFixture(t)
	mustExec(t, f.db, `
		INSERT INTO campaigns(id,org_id,name,subject,from_name,from_email,list_id,status,updated_at) VALUES(1,1,'C','S','F','f@one.test',1,'sending',now());
		INSERT INTO contacts(id,uuid,org_id,email,status,updated_at) VALUES
			(10,'00000000-0000-0000-0000-0000000000aa',1,'Reader@Example.test','active',now()),
			(11,'00000000-0000-0000-0000-0000000000bb',1,'other@example.test','active',now());
		INSERT INTO campaign_recipients(id,campaign_id,org_id,contact_id,email,status) VALUES(1,1,1,10,'Reader@Example.test','sent'),(2,1,1,11,'other@example.test','sent')`)
	return f
}

func TestCampaignUnsubscribeLinksRecipientOnce(t *testing.T) {
	f := campaignRecipientFixture(t)
	ctx := context.Background()
	token := f.compliance.UnsubscribeToken(10, 1, 1)
	for i := 0; i < 3; i++ {
		if err := f.compliance.ProcessOneClickUnsubscribe(ctx, token, "203.0.113.1", "mail"); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.compliance.ConfirmUnsubscribe(ctx, token, "", "203.0.113.1", "web"); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT unsubscribe_count FROM campaigns WHERE id=1`)
	count(t, f.db, 1, `SELECT count(*) FROM campaign_recipients WHERE id=1 AND unsubscribed_at IS NOT NULL`)
	count(t, f.db, 1, `SELECT count(*) FROM contacts WHERE id=10 AND status='unsubscribed'`)
	count(t, f.db, 1, `SELECT count(*) FROM suppressions WHERE source_type='campaign' AND source_id='1'`)
	count(t, f.db, 4, `SELECT count(*) FROM consent_audit WHERE contact_id=10 AND action='unsubscribe'`)

	// A recipient id from another org's token cannot touch this campaign.
	mustExec(t, f.db, `INSERT INTO contacts(id,org_id,email,status,updated_at) VALUES(30,2,'two@example.test','active',now())`)
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, f.compliance.UnsubscribeToken(30, 2, 2), "", ""); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 0, `SELECT count(*) FROM campaign_recipients WHERE id=2 AND unsubscribed_at IS NOT NULL`)
	count(t, f.db, 1, `SELECT unsubscribe_count FROM campaigns WHERE id=1`)
	// A contact id from another org is not unsubscribed (org-scoped lookup).
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, f.compliance.UnsubscribeToken(11, 2, 0), "", ""); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM contacts WHERE id=11 AND status='active'`)
}

func TestCampaignTestUnsubscribeTokenChangesNothing(t *testing.T) {
	f := campaignRecipientFixture(t)
	ctx := context.Background()
	token := f.compliance.TestUnsubscribeToken(1)
	if !f.compliance.ValidUnsubscribeToken(token) {
		t.Fatal("test token rejected")
	}
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, token, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.compliance.ConfirmUnsubscribe(ctx, token, "", "", ""); err != nil {
		t.Fatal(err)
	}
	page, err := f.compliance.GetUnsubscribePage(ctx, token)
	if err != nil || page["email"] != "test@example.com" {
		t.Fatalf("test page=%v err=%v", page, err)
	}
	count(t, f.db, 0, `SELECT count(*) FROM suppressions`)
	count(t, f.db, 0, `SELECT count(*) FROM consent_audit`)
	count(t, f.db, 0, `SELECT count(*) FROM contacts WHERE status<>'active'`)
	count(t, f.db, 0, `SELECT unsubscribe_count FROM campaigns WHERE id=1`)
}

func TestCampaignUnsubscribeAfterContactDeletion(t *testing.T) {
	f := campaignRecipientFixture(t)
	ctx := context.Background()
	mustExec(t, f.db, `DELETE FROM contacts WHERE id=10`)
	count(t, f.db, 1, `SELECT count(*) FROM campaign_recipients WHERE id=1 AND contact_id IS NULL`)
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, f.compliance.UnsubscribeToken(10, 1, 1), "", ""); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 1, `SELECT count(*) FROM suppressions WHERE email='Reader@Example.test'`)
	count(t, f.db, 1, `SELECT unsubscribe_count FROM campaigns WHERE id=1`)
	// A deleted contact without a recipient link never inserts an empty address.
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, f.compliance.UnsubscribeToken(404, 1, 0), "", ""); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 0, `SELECT count(*) FROM suppressions WHERE email=''`)
	count(t, f.db, 1, `SELECT count(*) FROM suppressions`)
}

func TestGDPRErasureOfCampaignRecipients(t *testing.T) {
	f := campaignRecipientFixture(t)
	ctx := context.Background()
	// Two erased contacts in the same campaign must not collide on the
	// (campaign_id, lower(email)) unique index.
	mustExec(t, f.db, `
		INSERT INTO campaign_events(campaign_id,recipient_id,event_type,user_agent,ip_address) VALUES(1,1,'open','UA','192.0.2.1'),(1,2,'open','UA2','192.0.2.2');
		INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(2,1,'admin@one.test','x',now());
		INSERT INTO campaign_test_sends(campaign_id,user_id,idempotency_key,request_hash,recipients,results) VALUES(1,1,'key-12345','h',ARRAY['reader@example.test'],'[{"email":"reader@example.test"}]'),(1,2,'key-67890','h',ARRAY['me@one.test'],'[]')`)
	actor := ContactActor{UserID: 1}
	for _, u := range []string{"00000000-0000-0000-0000-0000000000aa", "00000000-0000-0000-0000-0000000000bb"} {
		if err := f.compliance.DeleteContactData(ctx, 1, actor, u); err != nil {
			t.Fatal(err)
		}
	}
	count(t, f.db, 1, `SELECT count(*) FROM campaign_recipients WHERE id=1 AND email='erased+1@invalid' AND contact_id IS NULL`)
	count(t, f.db, 1, `SELECT count(*) FROM campaign_recipients WHERE id=2 AND email='erased+2@invalid' AND contact_id IS NULL`)
	count(t, f.db, 2, `SELECT count(*) FROM campaign_events WHERE ip_address IS NULL AND user_agent IS NULL`)
	count(t, f.db, 0, `SELECT count(*) FROM campaign_test_sends WHERE 'reader@example.test' = ANY(recipients) OR results::text LIKE '%reader@%'`)
	count(t, f.db, 1, `SELECT count(*) FROM campaign_test_sends WHERE recipients=ARRAY['me@one.test']`)

	// Unsubscribing from an erased recipient never stores the placeholder.
	if err := f.compliance.ProcessOneClickUnsubscribe(ctx, f.compliance.UnsubscribeToken(10, 1, 1), "", ""); err != nil {
		t.Fatal(err)
	}
	count(t, f.db, 0, `SELECT count(*) FROM suppressions WHERE email LIKE 'erased+%'`)
}
