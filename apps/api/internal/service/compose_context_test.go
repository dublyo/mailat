package service

import (
	"context"
	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/testutil"
	"testing"
)

func TestSESReplyForwardContextPreservesAliasThreadAndAttachments(t *testing.T) {
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, identity := mailboxFixture(t, db, "reply.test")
	var id string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,cc_emails,bcc_emails,reply_to,subject,text_body,html_body,envelope_recipients,"references",updated_at)
 SELECT $1,domain_id,$2,'<incoming@external.test>','from@external.test',ARRAY['sales@reply.test','other@external.test'],ARRAY['other@external.test','cc@external.test'],ARRAY['secret@external.test'],'help@external.test','Question','Original text','<p>Original <img src="cid:logo"></p>',ARRAY['sales@reply.test'],ARRAY['<prior@external.test>'],NOW() FROM identities WHERE id=$2 RETURNING uuid`, org, identity).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO email_attachments(received_email_id,filename,content_type,size_bytes,s3_bucket,s3_key,is_inline,content_id) SELECT id,'logo.png','image/png',5,'private','logo',true,'logo' FROM received_emails WHERE uuid=$1`, id); err != nil {
		t.Fatal(err)
	}
	svc := &ComposeService{db: db, cfg: &config.Config{EmailProvider: "ses"}}
	reply, err := svc.GetReplyContext(ctx, user, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if reply.From.Email != "sales@reply.test" || reply.InReplyTo != "<incoming@external.test>" || len(reply.References) != 2 || len(reply.To) != 2 || reply.To[0].Email != "help@external.test" || len(reply.Cc) != 1 || len(reply.Bcc) != 0 || len(reply.Attachments) != 1 {
		t.Fatalf("bad reply %+v", reply)
	}
	forward, err := svc.GetForwardContext(ctx, user, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Attachments) != 1 || forward.Attachments[0].BlobID == "" || forward.HTMLBody == "" || len(forward.To) != 0 || forward.InReplyTo != "" {
		t.Fatalf("bad forward %+v", forward)
	}
	var read bool
	if err = db.QueryRow(`SELECT is_read FROM received_emails WHERE uuid=$1`, id).Scan(&read); err != nil || read {
		t.Fatal("context read must not mark mail read", read, err)
	}
	_, other, _ := mailboxFixture(t, db, "foreign-reply.test")
	if _, err = svc.GetReplyContext(ctx, other, id, false); err == nil {
		t.Fatal("foreign message read")
	}
}
