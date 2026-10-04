package controller

import (
	"encoding/json"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
)

func TestSSEUsesFinalFolderAndOwningUser(t *testing.T) {
	c := NewSSEController()
	owner, foreign := make(chan *SSEEvent, 1), make(chan *SSEEvent, 1)
	c.registerClient(1, "owner", owner)
	c.registerClient(2, "foreign", foreign)
	defer c.unregisterClient(1, "owner")
	defer c.unregisterClient(2, "foreign")
	for _, folder := range []string{"dmarc-reports", "archive", "inbox", "spam"} {
		c.NotifyNewEmail(1, &model.ReceivedEmail{UUID: "message", IdentityID: 3, Folder: folder})
		select {
		case event := <-owner:
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatal(err)
			}
			var wire struct {
				Type       string `json:"type"`
				IdentityID int64  `json:"identityId"`
				Data       struct {
					Folder string `json:"folder"`
					UUID   string `json:"uuid"`
				} `json:"data"`
			}
			if err := json.Unmarshal(payload, &wire); err != nil {
				t.Fatal(err)
			}
			if wire.Type != "new_email" || wire.IdentityID != 3 || wire.Data.Folder != folder || wire.Data.UUID != "message" {
				t.Fatalf("client cannot identify final mailbox destination: %s", payload)
			}
		default:
			t.Fatal("owner did not receive the event")
		}
		select {
		case <-foreign:
			t.Fatal("received event leaked across accounts")
		default:
		}
	}
}
