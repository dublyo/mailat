package service

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/testutil"
)

// Two replicas each hold a LISTEN connection; one commit wakes both, and only
// at commit.
func TestMailboxNotifierWakesEveryReplicaAtCommit(t *testing.T) {
	db := testutil.Database(t)
	const owner = 918273 // distinct from other packages' fixtures sharing the channel
	if _, err := db.Exec(`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'Test','test',now());
		INSERT INTO users(id,org_id,email,password_hash,updated_at) VALUES(918273,1,'live@one.test','unused',now());
		INSERT INTO domains(id,org_id,name,verification_token,status,updated_at) VALUES(1,1,'one.test','t','active',now());
		INSERT INTO identities(id,user_id,domain_id,email,updated_at) VALUES(1,918273,1,'live@one.test',now())`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var running sync.WaitGroup
	defer func() { cancel(); running.Wait() }()
	type replica struct {
		n     *MailboxNotifier
		woken chan struct{}
	}
	replicas := []replica{{NewMailboxNotifier(os.Getenv("MAILAT_TEST_DATABASE_URL")), make(chan struct{}, 8)}, {NewMailboxNotifier(os.Getenv("MAILAT_TEST_DATABASE_URL")), make(chan struct{}, 8)}}
	for _, r := range replicas {
		woken := r.woken
		r.n.SetHandlers(func(id int64) {
			if id == owner {
				woken <- struct{}{}
			}
		}, func() {})
		running.Add(1)
		go func(n *MailboxNotifier) { defer running.Done(); n.Run(ctx) }(r.n)
	}
	deadline := time.Now().Add(10 * time.Second)
	for !replicas[0].n.Healthy() || !replicas[1].n.Healthy() {
		if time.Now().After(deadline) {
			t.Fatal("listeners never connected")
		}
		time.Sleep(20 * time.Millisecond)
	}

	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO received_emails(uuid,org_id,domain_id,identity_id,message_id,from_email,subject,updated_at) VALUES(gen_random_uuid(),1,1,1,'live-1','s@example.test','Live',now())`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-replicas[0].woken:
		t.Fatal("woken before commit")
	case <-time.After(200 * time.Millisecond):
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i, r := range replicas {
		select {
		case <-r.woken:
		case <-time.After(5 * time.Second):
			t.Fatalf("replica %d missed the commit", i)
		}
	}
	cancel()
	running.Wait()
	if replicas[0].n.Healthy() {
		t.Fatal("a stopped listener reports healthy")
	}
}
