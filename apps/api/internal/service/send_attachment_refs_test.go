package service

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestStorageCleanupKeepsObjectsReferencedByQueuedSends(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, _, _ := mailboxFixture(t, db, "refs-cleanup.test")
	var emailID int64
	if err := db.QueryRow(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES($1,'<refs@refs-cleanup.test>','a@refs-cleanup.test','b@elsewhere.test','s','queued',now()) RETURNING id`, org).Scan(&emailID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO send_attachment_refs VALUES($1,'private-test','attachments/1')`, emailID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO storage_cleanup_jobs(bucket,object_key) VALUES('private-test','attachments/1')`); err != nil {
		t.Fatal(err)
	}
	storage := &fakeIncomingStorage{}
	receive := &ReceivingService{db: db, storage: storage}
	if err := receive.cleanupStorage(ctx); err != nil {
		t.Fatal(err)
	}
	if storage.deletes != 0 {
		t.Fatal("deleted an object a queued send still references")
	}
	if _, err := db.Exec(`DELETE FROM send_attachment_refs; UPDATE storage_cleanup_jobs SET next_attempt_at=now()`); err != nil {
		t.Fatal(err)
	}
	if err := receive.cleanupStorage(ctx); err != nil {
		t.Fatal(err)
	}
	if storage.deletes != 1 {
		t.Fatalf("unreferenced object kept: %d deletes", storage.deletes)
	}
}
