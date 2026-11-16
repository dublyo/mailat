package service

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/testutil"
)

// The sidebar split lists only addresses the caller can read, counts only the
// Inbox folder, and never includes another user's mailbox.
func TestMailboxScopesSplitInboxByDomainAndAddress(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	ctx := context.Background()
	// identity 1 = user 1, a@one.test (domain 1); identity 2 = user 2, b@one.test;
	// identity 3 = user 1, catch@two.test (domain 2, catch-all); identity 4 = other org.
	_, err := db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,folder,is_read,is_archived,direction,received_at,updated_at) VALUES
	 (1,1,1,'s1','x@example.test',ARRAY['a@one.test'],'one unread','inbox',false,false,'inbound',NOW(),NOW()),
	 (1,1,1,'s2','x@example.test',ARRAY['a@one.test'],'one unread 2','inbox',false,false,'inbound',NOW(),NOW()),
	 (1,1,1,'s3','x@example.test',ARRAY['a@one.test'],'one read','inbox',true,false,'inbound',NOW(),NOW()),
	 (1,1,1,'s4','x@example.test',ARRAY['a@one.test'],'one archived','inbox',false,true,'inbound',NOW(),NOW()),
	 (1,1,1,'s5','x@example.test',ARRAY['a@one.test'],'one report','dmarc-reports',false,false,'inbound',NOW(),NOW()),
	 (1,2,3,'s6','x@example.test',ARRAY['sales@two.test'],'two unread','inbox',false,false,'inbound',NOW(),NOW()),
	 (1,1,2,'s7','x@example.test',ARRAY['b@one.test'],'other user','inbox',false,false,'inbound',NOW(),NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	inbox := &InboxService{db: db}
	scopes, err := inbox.GetMailboxScopes(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if scopes.Total != 4 || scopes.Unread != 3 || len(scopes.Domains) != 2 {
		t.Fatalf("unified counts or domains wrong: %+v", scopes)
	}
	one, two := scopes.Domains[0], scopes.Domains[1]
	if one.Name != "one.test" || one.Total != 3 || one.Unread != 2 || len(one.Identities) != 1 || one.Identities[0].Email != "a@one.test" || one.Identities[0].Unread != 2 {
		t.Fatalf("one.test scope wrong (another user's address or non-Inbox mail leaked?): %+v", one)
	}
	if two.Name != "two.test" || two.Total != 1 || two.Unread != 1 || len(two.Identities) != 1 || !two.Identities[0].IsCatchAll {
		t.Fatalf("two.test scope wrong: %+v", two)
	}

	// The same scopes are valid count filters; a domain without a readable
	// address is refused rather than counted.
	counts, err := inbox.GetReceivedEmailCounts(ctx, 1, 0, two.DomainID)
	if err != nil || counts.Inbox != 1 || counts.InboxUnread != 1 {
		t.Fatalf("domain counts wrong: %+v %v", counts, err)
	}
	if _, err = inbox.GetReceivedEmailCounts(ctx, 1, 0, 3); err == nil {
		t.Fatal("counts for another organization's domain were allowed")
	}

	// User 2 sees only their own address and mail.
	scopes, err = inbox.GetMailboxScopes(ctx, 2)
	if err != nil || scopes.Total != 1 || len(scopes.Domains) != 1 || len(scopes.Domains[0].Identities) != 1 || scopes.Domains[0].Identities[0].Email != "b@one.test" {
		t.Fatalf("user 2 scopes wrong: %+v %v", scopes, err)
	}
}
