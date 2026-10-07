package service

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/testutil"
)

// Push subscriptions belong to the user, so signing out everywhere must stop
// pushes to the devices that were signed out.
func TestRevokeAllSessionsPausesPush(t *testing.T) {
	db := testutil.Database(t)
	org, user, _ := mailboxFixture(t, db, "sessions.test")
	var other int64
	if err := db.QueryRow(`INSERT INTO users(org_id,email,password_hash,updated_at) VALUES($1,'other@sessions.test','x',now()) RETURNING id`, org).Scan(&other); err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"current", "laptop"} {
		if _, err := db.Exec(`INSERT INTO user_sessions(user_id,org_id,token_hash,expires_at) VALUES($1,$2,$3,now()+interval '1 day')`, user, org, hashToken(token)); err != nil {
			t.Fatal(err)
		}
	}
	for i, owner := range []int64{user, user, other} {
		if _, err := db.Exec(`INSERT INTO push_subscriptions(user_id,endpoint,p256dh_key,auth_key) VALUES($1,$2,'k','a')`, owner, "https://push.example.test/"+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	svc := NewSessionService(db, nil)
	if n, err := svc.RevokeAllSessions(context.Background(), user, "current"); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if n := count(`SELECT count(*) FROM push_subscriptions WHERE user_id=$1 AND active`, user); n != 0 {
		t.Fatalf("%d devices still receive pushes after signing out everywhere", n)
	}
	if n := count(`SELECT count(*) FROM push_subscriptions WHERE user_id=$1 AND active`, other); n != 1 {
		t.Fatal("another user's push was paused")
	}
	if n := count(`SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`, user); n != 1 {
		t.Fatal("the current session must stay", n)
	}

	if _, err := db.Exec(`UPDATE push_subscriptions SET active=true`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RevokeAllUserSessions(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM push_subscriptions WHERE user_id=$1 AND active`, user); n != 0 {
		t.Fatal("push kept after revoking every session", n)
	}
	if n := count(`SELECT count(*) FROM user_sessions WHERE user_id=$1 AND active`, user); n != 0 {
		t.Fatal(n)
	}
}
