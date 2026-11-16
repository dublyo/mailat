package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
)

func TestHealthSummaryRecordedCountsAndJSONContract(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	_, err := db.Exec(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,subject,direction,send_status,is_spam,virus_verdict,is_read,received_at,updated_at)
	 VALUES
	 (1,1,1,'health-1','sender@example.test',ARRAY['a@one.test'],'normal','inbound','received',false,'PASS',false,NOW(),NOW()),
	 (1,1,1,'health-2','sender@example.test',ARRAY['a@one.test'],'flagged','inbound','received',true,'FAIL',true,NOW(),NOW()),
	 (1,1,1,'health-old','sender@example.test',ARRAY['a@one.test'],'old','inbound','received',true,'FAIL',true,NOW()-INTERVAL '40 days',NOW()),
	 (1,1,1,'health-draft','a@one.test',ARRAY['to@example.test'],'draft','outbound','draft',false,'PASS',true,NOW(),NOW())`)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := NewHealthService(db, &config.Config{}).GetEmailHealthSummary(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	r := summary.ReceivingMetrics
	if r.TotalReceived != 2 || r.TotalSpam != 1 || r.TotalVirus != 1 || r.TotalRead != 1 {
		t.Fatalf("incorrect received period/direction counts: %+v", r)
	}
	if summary.HealthStatus != "unknown" || summary.SendingMetrics.TotalSent != 0 {
		t.Fatal("empty sending history was treated as healthy")
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]map[string]interface{}
	// Extract just the metrics because other summary fields include arrays/scalars.
	var top map[string]json.RawMessage
	if err = json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	payload = make(map[string]map[string]interface{})
	for _, name := range []string{"sendingMetrics", "receivingMetrics"} {
		var fields map[string]interface{}
		if err = json.Unmarshal(top[name], &fields); err != nil {
			t.Fatal(err)
		}
		payload[name] = fields
	}
	for _, name := range []string{"totalDelivered", "totalBounced", "totalComplaints", "totalFailed"} {
		if value, ok := payload["sendingMetrics"][name]; !ok || value != float64(0) {
			t.Fatalf("missing zero-valued API field %s", name)
		}
	}
}

// A display-name sender ("Name <a@example.com>") counts under its real domain,
// never as a separate "example.com>" entry.
func TestReputationByDomainIgnoresDisplayNames(t *testing.T) {
	db := testutil.Database(t)
	seedReceivedFixture(t, db)
	_, err := db.Exec(`INSERT INTO transactional_emails(org_id,message_id,from_address,to_addresses,subject,status,updated_at) VALUES
	 (1,'<h1@one.test>','Ketab Aziz <info@One.test>','b@elsewhere.test','s','sent',now()),
	 (1,'<h2@one.test>','info@one.test','b@elsewhere.test','s','delivered',now()),
	 (1,'<h3@one.test>','"Support, Team" <help@one.test> ','b@elsewhere.test','s','bounced',now())`)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := NewHealthService(db, &config.Config{}).GetReputationMetrics(context.Background(), 1, "30days")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := metrics.ByDomain["one.test"]
	if !ok || got.Sent != 3 || got.Delivered != 1 || got.Bounced != 1 || len(metrics.ByDomain) != 1 {
		t.Fatalf("display-name senders split the domain: %+v", metrics.ByDomain)
	}
}
