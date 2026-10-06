package controller

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/service"
)

func TestCampaignErrorDetails(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		msg    string
	}{
		{service.ErrCampaignNotFound, http.StatusNotFound, "Campaign not found"},
		{service.ErrCampaignForbidden, http.StatusForbidden, service.ErrCampaignForbidden.Error()},
		{&service.CampaignStateError{Message: "campaign content is locked after sending started"}, http.StatusConflict, "campaign content is locked after sending started"},
		{service.ErrCampaignTestConflict, http.StatusConflict, service.ErrCampaignTestConflict.Error()},
		{service.ErrCampaignTestRateLimited, http.StatusTooManyRequests, service.ErrCampaignTestRateLimited.Error()},
		{fmt.Errorf("wrap: %w", &provider.MailValidationError{Message: "set the postal address"}), http.StatusBadRequest, "set the postal address"},
		{errors.New("pq: relation secret_table does not exist"), http.StatusInternalServerError, "Campaign request failed"},
	} {
		status, msg := campaignErrorDetails(tc.err)
		if status != tc.status || msg != tc.msg {
			t.Errorf("%v: got %d %q", tc.err, status, msg)
		}
	}
}
