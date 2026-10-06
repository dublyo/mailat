package service

import (
	"time"

	"github.com/dublyo/mailat/api/internal/provider"
)

// Campaign send outcomes. Only rejected and uncertain are final for the
// recipient; the others release the row to pending and act on the campaign.
const (
	sendOutcomeOK             = "ok"
	sendOutcomeThrottle       = "throttle"        // pending; campaign throttled_until
	sendOutcomeProviderPaused = "provider_paused" // pending; pause provider_paused
	sendOutcomeSender         = "sender"          // pending; pause sender_unavailable
	sendOutcomeRejected       = "rejected"        // failed
	sendOutcomeUncertain      = "uncertain"       // unknown, never resent
)

// classifySESSendError builds on provider.ClassifySendError. SES reports account
// pauses and sender problems as client faults, which that function calls
// rejected; for campaigns they mean "not accepted, retry after the pause".
// The codes are matched before the fault check because they prove refusal.
func classifySESSendError(err error) string {
	if err == nil {
		return sendOutcomeOK
	}
	class := provider.ClassifySendError(err)
	if class == provider.SendThrottled {
		return sendOutcomeThrottle
	}
	switch provider.SESErrorCode(err) {
	case "AccountSuspendedException", "SendingPausedException":
		return sendOutcomeProviderPaused
	case "MailFromDomainNotVerifiedException", "NotFoundException":
		return sendOutcomeSender
	}
	if class == provider.SendRejected {
		return sendOutcomeRejected
	}
	return sendOutcomeUncertain
}

// campaignThrottleDelay is how long a throttled campaign waits: long when the
// daily quota is spent, short for a per-second rate throttle.
func campaignThrottleDelay(err error) time.Duration {
	if provider.IsQuotaExhausted(err) {
		return 30 * time.Minute
	}
	return time.Minute
}
