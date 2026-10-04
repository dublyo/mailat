package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/gogf/gf/v2/frame/g"
	"testing"
)

// Keep runnable documentation tied to the server DTOs: strict decoding catches
// plausible but silently ignored SDK/example fields (filename vs name, labels vs addLabels).
func TestCoreExamplesUseServerDTOs(t *testing.T) {
	cases := []struct {
		verb, path string
		dto        interface{}
	}{
		{"POST", "/emails", &model.SendEmailRequest{}},
		{"POST", "/emails/batch", &model.BatchSendRequest{}},
		{"POST", "/compose/send", &model.ComposeEmailRequest{}},
		{"POST", "/compose/drafts", &model.SaveDraftRequest{}},
		{"PUT", "/compose/drafts/:id", &model.SaveDraftRequest{}},
		{"POST", "/inbox/received/mark", &model.MarkEmailsRequest{}},
		{"POST", "/inbox/received/labels", &model.LabelEmailsRequest{}},
		{"POST", "/inbox/received/move", &model.MoveEmailsRequest{}},
		{"PUT", "/settings", &service.UpdateSettingsRequest{}},
		{"POST", "/webhooks", &model.CreateWebhookRequest{}},
		{"POST", "/domains", &model.CreateDomainRequest{}},
		{"POST", "/identities", &model.CreateIdentityRequest{}},
	}
	for _, tc := range cases {
		t.Run(tc.verb+tc.path, func(t *testing.T) {
			media := object{}
			op := object{"requestBody": object{"content": object{"application/json": media}}}
			customize(op, tc.verb, tc.path)
			example, ok := media["example"]
			if !ok {
				t.Fatal("missing example")
			}
			data, err := json.Marshal(example)
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err = decoder.Decode(tc.dto); err != nil {
				t.Fatal(err)
			}
			if err = g.Validator().Data(tc.dto).Run(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch req := tc.dto.(type) {
			case *model.SendEmailRequest:
				if len(req.Attachments) != 1 || req.Attachments[0].Name != "hello.txt" || req.Attachments[0].Type != "text/plain" {
					t.Fatal("attachment contract")
				}
			case *model.LabelEmailsRequest:
				if len(req.AddLabels) != 1 {
					t.Fatal("label action missing")
				}
			case *model.CreateIdentityRequest:
				if req.DomainId == "" || req.Email == "" {
					t.Fatal("identity contract")
				}
			case *service.UpdateSettingsRequest:
				if req.AutoOrganizeDMARCReports == nil || *req.AutoOrganizeDMARCReports {
					t.Fatal("explicit opt-out lost")
				}
			case *model.MoveEmailsRequest:
				if req.Folder != "dmarc-reports" {
					t.Fatal("report folder example missing")
				}
			}
		})
	}
}
