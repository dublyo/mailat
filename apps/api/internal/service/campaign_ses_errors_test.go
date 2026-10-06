package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	"github.com/dublyo/mailat/api/internal/provider"
)

func TestClassifySESSendError(t *testing.T) {
	msg := func(s string) *string { return &s }
	api := func(code string, fault smithy.ErrorFault) error {
		return &smithy.GenericAPIError{Code: code, Message: "x", Fault: fault}
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, sendOutcomeOK},
		{"too many requests", &types.TooManyRequestsException{Message: msg("Maximum sending rate exceeded")}, sendOutcomeThrottle},
		{"limit exceeded", &types.LimitExceededException{}, sendOutcomeThrottle},
		{"generic throttling", api("Throttling", smithy.FaultClient), sendOutcomeThrottle},
		{"daily quota", &types.TooManyRequestsException{Message: msg("Daily message quota exceeded")}, sendOutcomeThrottle},
		{"generic too many", api("TooManyRequestsException", smithy.FaultClient), sendOutcomeThrottle},
		{"account suspended", &types.AccountSuspendedException{}, sendOutcomeProviderPaused},
		{"sending paused", &types.SendingPausedException{}, sendOutcomeProviderPaused},
		{"sending paused, unknown fault", api("SendingPausedException", smithy.FaultUnknown), sendOutcomeProviderPaused},
		{"wrapped paused", fmt.Errorf("SES send: %w", &types.SendingPausedException{}), sendOutcomeProviderPaused},
		{"mail from unverified", &types.MailFromDomainNotVerifiedException{}, sendOutcomeSender},
		{"identity not found", &types.NotFoundException{}, sendOutcomeSender},
		{"generic not found", api("NotFoundException", smithy.FaultClient), sendOutcomeSender},
		{"message rejected", &types.MessageRejected{Message: msg("Email address is not verified")}, sendOutcomeRejected},
		{"bad request", &types.BadRequestException{}, sendOutcomeRejected},
		{"validation", &provider.MailValidationError{Message: "bad"}, sendOutcomeRejected},
		{"timeout", context.DeadlineExceeded, sendOutcomeUncertain},
		{"cancelled", context.Canceled, sendOutcomeUncertain},
		{"network", &net.OpError{Op: "dial", Err: errors.New("connection reset")}, sendOutcomeUncertain},
		{"server fault", api("InternalFailure", smithy.FaultServer), sendOutcomeUncertain},
		{"unknown fault", api("SomethingNew", smithy.FaultUnknown), sendOutcomeUncertain},
		{"plain error", errors.New("boom"), sendOutcomeUncertain},
	}
	for _, tc := range cases {
		if got := classifySESSendError(tc.err); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func TestCampaignThrottleDelay(t *testing.T) {
	msg := func(s string) *string { return &s }
	for _, tc := range []struct {
		err  error
		want time.Duration
	}{
		{&types.TooManyRequestsException{Message: msg("Maximum sending rate exceeded")}, time.Minute},
		{&types.TooManyRequestsException{Message: msg("Daily message quota exceeded")}, 30 * time.Minute},
		{&smithy.GenericAPIError{Code: "Throttling", Message: "Daily message quota exceeded", Fault: smithy.FaultClient}, 30 * time.Minute},
		{&types.LimitExceededException{}, 30 * time.Minute},
	} {
		if got := campaignThrottleDelay(tc.err); got != tc.want {
			t.Errorf("%v: got %v want %v", tc.err, got, tc.want)
		}
	}
}
