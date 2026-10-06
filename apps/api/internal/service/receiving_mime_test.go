package service

import (
	"github.com/dublyo/mailat/api/internal/model"
	"strings"
	"testing"
)

func TestIncomingMIMENestedBodiesAndAttachment(t *testing.T) {
	raw := "From: =?UTF-8?B?2YXYsdit2KjYpw==?= <sender@example.com>\r\nTo: one@example.com\r\nSubject: =?UTF-8?B?SGVsbG8=?=\r\nContent-Type: multipart/mixed; boundary=outer\r\n\r\n--outer\r\nContent-Type: multipart/alternative; boundary=inner\r\n\r\n--inner\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\ncaf=E9\r\n--inner\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\nPHA+aGVsbG88L3A+\r\n--inner--\r\n--outer\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=sample.bin\r\nContent-Transfer-Encoding: base64\r\n\r\nAAEC/w==\r\n--outer--\r\n"
	result, err := parseIncomingMIME([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "café" || result.HTML != "<p>hello</p>" {
		t.Fatalf("wrong bodies: %q %q", result.Text, result.HTML)
	}
	if len(result.Attachments) != 1 || string(result.Attachments[0].Data) != "\x00\x01\x02\xff" {
		t.Fatalf("attachment decoding failed: %#v", result.Attachments)
	}
	if decodeMIMEHeader(result.Header.Get("Subject")) != "Hello" {
		t.Fatal("encoded subject not decoded")
	}
}
func TestIncomingMIMERejectsMalformedMultipart(t *testing.T) {
	if _, err := parseIncomingMIME([]byte("Content-Type: multipart/mixed\r\n\r\nbody")); err == nil {
		t.Fatal("accepted missing boundary")
	}
}
func TestReceivedListFilters(t *testing.T) {
	yes := true
	query, args, err := receivedListQuery(9, &model.InboxListRequest{IdentityID: 3, DomainID: 7, Folder: "all", HasAttachments: &yes, Search: "100%_x", Sender: "alice", DateFrom: "2026-10-01", DateTo: "2026-10-03"})
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{"re.mailbox_owner_id=$1", "re.identity_id=$2", "re.domain_id=$3", "re.has_attachments=$4", "re.received_at<"} {
		if !strings.Contains(query, part) {
			t.Fatalf("missing %s in %s", part, query)
		}
	}
	if args[4] != "%100\\%\\_x%" {
		t.Fatalf("search wildcards not escaped: %#v", args[4])
	}
	if _, _, err = receivedListQuery(9, &model.InboxListRequest{DateFrom: "nonsense"}); err == nil {
		t.Fatal("invalid date accepted")
	}
}
