package service

import (
	"regexp"
	"strings"
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

// campaignThrottleDelay is how long a throttled campaign waits: 30 minutes
// when the message mentions the daily quota ("Daily message quota exceeded"),
// otherwise one minute. Unlike provider.IsQuotaExhausted, the error code alone
// (LimitExceededException) does not mean the day's quota is spent.
func campaignThrottleDelay(err error) time.Duration {
	if provider.ClassifySendError(err) == provider.SendThrottled {
		text := strings.ToLower(err.Error())
		if strings.Contains(text, "daily") || strings.Contains(text, "quota") {
			return 30 * time.Minute
		}
	}
	return time.Minute
}

// sendErrorText is a provider error as stored on a message row: the recipient
// address (SES sandbox rejections quote it) is replaced, and it fits 500 runes.
func sendErrorText(err error, email string) string {
	text := err.Error()
	if email != "" {
		text = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(email)).ReplaceAllLiteralString(text, "[recipient]")
	}
	return truncateRunes(text, 500)
}
