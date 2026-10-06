package worker

import (
	"context"
	"testing"

	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestBounceRatePauseRecordsCampaignReason(t *testing.T) {
	db := testutil.Database(t)
	for _, q := range []string{
		`INSERT INTO organizations(id,name,slug,updated_at) VALUES(1,'QA','qa',now())`,
		`INSERT INTO lists(id,org_id,name,updated_at) VALUES(1,1,'Readers',now())`,
		`INSERT INTO campaigns(org_id,list_id,name,subject,from_name,from_email,status,updated_at) VALUES
			(1,1,'Live','s','n','a@example.test','sending',now()),(1,1,'Draft','s','n','a@example.test','draft',now())`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	(&ScheduledTaskHandler{db: db}).pauseWarmupForBounceRate(context.Background(), 1, 12.5)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM campaigns WHERE status='paused' AND status_reason='bounce_rate_high' AND name='Live'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("paused=%d err=%v", n, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM campaigns WHERE status='draft' AND status_reason IS NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("draft untouched=%d err=%v", n, err)
	}
}
