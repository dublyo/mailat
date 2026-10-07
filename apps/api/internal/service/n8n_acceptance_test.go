package service

// This opt-in fixture executes real mail services, but never constructs an AWS
// client. It is intentionally separate from controller/auth acceptance tests.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
	"github.com/dublyo/mailat/api/internal/eventoutbox"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/testutil"
)

type n8nProvider struct {
	provider.EmailProvider
	mu       sync.Mutex
	messages []*provider.EmailMessage
}

func (p *n8nProvider) Name() string { return "ses" }
func (p *n8nProvider) SendEmail(_ context.Context, m *provider.EmailMessage) (*provider.SendResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.messages = append(p.messages, m)
	return &provider.SendResult{Success: true, MessageID: fmt.Sprintf("n8n-fake-%d", len(p.messages))}, nil
}

type n8nStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (s *n8nStorage) Put(_ context.Context, b, k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[b+"/"+k] = append([]byte{}, v...)
	return nil
}
func (s *n8nStorage) Get(_ context.Context, b, k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.objects[b+"/"+k]
	if !ok {
		return nil, fmt.Errorf("missing fixture object")
	}
	return append([]byte{}, v...), nil
}
func (s *n8nStorage) GetEmailFromS3(c context.Context, b, k string) ([]byte, error) {
	return s.Get(c, b, k)
}
func (s *n8nStorage) PutAttachment(c context.Context, b, k, _ string, v []byte) error {
	return s.Put(c, b, k, v)
}
func (s *n8nStorage) DeleteObject(context.Context, string, string) error { return nil }
func (s *n8nStorage) GenerateDownloadURL(context.Context, string, string, string) (string, error) {
	return "", fmt.Errorf("fixture only supports private byte download")
}

func TestN8NAcceptanceFixture(t *testing.T) {
	statePath := os.Getenv("MAILAT_N8N_STATE")
	if statePath == "" {
		t.Skip("opt in with MAILAT_N8N_STATE and the local n8n acceptance harness")
	}
	db := testutil.Database(t)
	ctx := context.Background()
	org, user, identity := mailboxFixture(t, db, "n8n.test")
	var message string
	err := db.QueryRow(`INSERT INTO received_emails(org_id,domain_id,identity_id,message_id,from_email,to_emails,envelope_recipients,reply_to,subject,text_body,html_body,has_attachments,updated_at) SELECT $1,domain_id,$2,'<question@external.test>','customer@external.test',ARRAY['sales@n8n.test'],ARRAY['sales@n8n.test'],'help@external.test','Question','Original text','<p>Original<img src="cid:logo"></p>',true,NOW() FROM identities WHERE id=$2 RETURNING uuid`, org, identity).Scan(&message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO email_attachments(received_email_id,filename,content_type,size_bytes,s3_bucket,s3_key,is_inline,content_id) SELECT id,'logo.txt','text/plain',10,'private-test','logo',true,'logo' FROM received_emails WHERE uuid=$1`, message); err != nil {
		t.Fatal(err)
	}
	storage := &n8nStorage{objects: map[string][]byte{"private-test/logo": []byte("logo bytes")}}
	p := &n8nProvider{}
	cfg := &config.Config{EmailProvider: "ses", DisableAppLimits: true}
	compose := &ComposeService{db: db, cfg: cfg, emailProvider: p, attachments: storage}
	transactional := &TransactionalService{db: db, cfg: cfg, emailProvider: p, attachments: storage}
	inbox := &InboxService{db: db}
	receiving := &ReceivingService{db: db, storage: storage}
	if _, err = inbox.SaveLabel(ctx, user, org, "", "Automated", ""); err != nil {
		t.Fatal(err)
	}
	if err = eventoutbox.Emit(ctx, db, eventoutbox.Event{Type: "email.received", OrgID: org, UserID: user, IdentityID: identity, MessageUUID: message, DedupeKey: "fixture-incoming", Data: map[string]any{"subject": "Question"}}); err != nil {
		t.Fatal(err)
	}
	var payload json.RawMessage
	if err = db.QueryRow(`SELECT payload FROM webhook_events WHERE org_id=$1 AND event_type='email.received'`, org).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var once sync.Once
	var downloads atomic.Int64
	respond := func(w http.ResponseWriter, v any, e error) {
		w.Header().Set("Content-Type", "application/json")
		if e != nil {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]any{"error": e.Error()})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": v})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer n8n-disposable-fixture-key" {
			w.WriteHeader(401)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
		var v any
		var e error
		switch {
		case strings.HasPrefix(path, "inbox/received/") && strings.Contains(path, "/attachments/"):
			parts := strings.Split(path, "/")
			var data []byte
			var filename, kind string
			data, filename, kind, e = receiving.AttachmentDownload(r.Context(), user, parts[2], parts[4])
			if e == nil {
				downloads.Add(1)
				w.Header().Set("Content-Type", kind)
				w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
				w.Write(data)
				return
			}
		case strings.HasPrefix(path, "compose/reply/"):
			v, e = compose.GetReplyContext(r.Context(), user, strings.TrimPrefix(path, "compose/reply/"), false)
		case path == "compose/send":
			var req struct {
				ComposeEmail
				FromEmail string `json:"fromEmail"`
			}
			e = json.NewDecoder(r.Body).Decode(&req)
			if e == nil {
				req.From = EmailAddress{Email: req.FromEmail}
				req.SubmissionKey = r.Header.Get("Idempotency-Key")
				v, e = compose.SendEmail(r.Context(), user, &req.ComposeEmail)
			}
		case path == "inbox/received/labels":
			var req struct {
				EmailUUIDs []string `json:"emailUuids"`
				Add        []string `json:"addLabels"`
				Remove     []string `json:"removeLabels"`
			}
			e = json.NewDecoder(r.Body).Decode(&req)
			if e == nil {
				e = inbox.LabelReceivedEmails(r.Context(), user, req.EmailUUIDs, req.Add, req.Remove)
			}
		case path == "inbox/received/move":
			var req model.MoveEmailsRequest
			e = json.NewDecoder(r.Body).Decode(&req)
			if e == nil {
				e = inbox.MoveReceivedEmails(r.Context(), user, req.EmailUUIDs, req.Folder)
			}
		case strings.HasPrefix(path, "inbox/received/"):
			v, e = inbox.GetReceivedEmail(r.Context(), user, strings.TrimPrefix(path, "inbox/received/"))
		case path == "emails/batch":
			var req model.BatchSendRequest
			e = json.NewDecoder(r.Body).Decode(&req)
			if e == nil {
				req.IdempotencyKey = r.Header.Get("Idempotency-Key")
				v, e = transactional.BatchSendEmailForUser(r.Context(), org, SendActor{UserID: user, Admin: true}, &req)
			}
		case path == "emails":
			var req model.SendEmailRequest
			e = json.NewDecoder(r.Body).Decode(&req)
			if e == nil {
				req.IdempotencyKey = r.Header.Get("Idempotency-Key")
				v, e = transactional.SendEmailForUser(r.Context(), org, SendActor{UserID: user, Admin: true}, &req)
			}
		case strings.HasPrefix(path, "emails/"):
			v, e = transactional.GetEmailStatusForUser(r.Context(), org, user, strings.TrimPrefix(path, "emails/"))
		default:
			w.WriteHeader(404)
			return
		}
		respond(w, v, e)
	})
	mux.HandleFunc("/fixture/state", func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		calls := len(p.messages)
		p.mu.Unlock()
		email, e := inbox.GetReceivedEmail(ctx, user, message)
		respond(w, map[string]any{"calls": calls, "downloads": downloads.Load(), "email": email}, e)
	})
	mux.HandleFunc("/fixture/finish", func(w http.ResponseWriter, r *http.Request) { once.Do(func() { close(done) }); respond(w, true, nil) })
	server := httptest.NewServer(mux)
	defer server.Close()
	state := map[string]any{"baseUrl": server.URL + "/api/v1", "fixtureUrl": server.URL, "token": "n8n-disposable-fixture-key", "secret": "n8n-disposable-webhook-secret", "messageUuid": message, "event": payload}
	encoded, _ := json.MarshalIndent(state, "", "  ")
	if err = os.WriteFile(statePath, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(30 * time.Minute):
		t.Fatal("n8n acceptance fixture timed out")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.messages) != 4 {
		t.Fatalf("expected reply + single + two batch sends exactly once; got %d", len(p.messages))
	}
	sender, senderErr := mail.ParseAddress(p.messages[0].From)
	if senderErr != nil || sender.Address != "sales@n8n.test" || p.messages[0].To[0] != "help@external.test" || len(p.messages[0].Attachments) != 1 || string(p.messages[0].Attachments[0].Data) != "logo bytes" {
		t.Fatalf("reply lost alias/context/attachment: %+v", p.messages[0])
	}
	messageState, err := inbox.GetReceivedEmail(ctx, user, message)
	if err != nil || messageState.Folder != "archive" || messageState.IsRead || len(messageState.Labels) != 1 || messageState.Labels[0] != "Automated" {
		t.Fatalf("wrong final inbox state: %+v %v", messageState, err)
	}
	if downloads.Load() != 1 {
		t.Fatalf("expected one download, got %d", downloads.Load())
	}
}
