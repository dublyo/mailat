package provider

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func httpError(status int, inner error) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}}, Err: inner}}
}

func TestClassifySendError(t *testing.T) {
	msg := func(s string) *string { return &s }
	cases := []struct {
		name string
		err  error
		want SendErrorClass
	}{
		{"too many requests", &types.TooManyRequestsException{Message: msg("slow down")}, SendThrottled},
		{"limit exceeded", &types.LimitExceededException{}, SendThrottled},
		{"wrapped throttle", fmt.Errorf("operation SendEmail: %w", &types.TooManyRequestsException{}), SendThrottled},
		{"generic throttling code", &smithy.GenericAPIError{Code: "Throttling", Fault: smithy.FaultClient}, SendThrottled},
		{"http 429", httpError(429, errors.New("rate exceeded")), SendThrottled},
		{"message rejected", &types.MessageRejected{}, SendRejected},
		{"sending paused", &types.SendingPausedException{}, SendRejected},
		{"account suspended", &types.AccountSuspendedException{}, SendRejected},
		{"validation", &MailValidationError{Message: "bad"}, SendRejected},
		{"http 500", httpError(500, errors.New("boom")), SendUncertain},
		{"deadline", context.DeadlineExceeded, SendUncertain},
		{"server fault", &smithy.GenericAPIError{Code: "InternalFailure", Fault: smithy.FaultServer}, SendUncertain},
	}
	for _, tc := range cases {
		if got := ClassifySendError(tc.err); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
	if IsDefinitiveSendError(&types.TooManyRequestsException{}) {
		t.Error("throttle must not be definitive")
	}
	if !IsDefinitiveSendError(&types.MessageRejected{}) {
		t.Error("MessageRejected must be definitive")
	}
}

func TestIsQuotaExhausted(t *testing.T) {
	msg := func(s string) *string { return &s }
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{&types.LimitExceededException{}, true},
		{&types.TooManyRequestsException{Message: msg("Daily message quota exceeded")}, true},
		{&types.TooManyRequestsException{Message: msg("Maximum sending rate exceeded")}, false},
		{&types.MessageRejected{Message: msg("daily quota")}, false},
		{context.DeadlineExceeded, false},
	} {
		if got := IsQuotaExhausted(tc.err); got != tc.want {
			t.Errorf("%v: got %v want %v", tc.err, got, tc.want)
		}
	}
}
