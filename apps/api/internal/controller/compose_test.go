package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/provider"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/lib/pq"
)

func TestComposeErrorClassificationKeepsUncertainSubmissionKey(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, errors.New("temporary storage failure"), &pq.Error{Code: "42P01", Message: "receipt table unavailable"}} {
		status, message := composeErrorDetails(err)
		if status != http.StatusServiceUnavailable || !strings.Contains(message, "same submission key") {
			t.Fatalf("infrastructure failure would release the client's send key: %d %q", status, message)
		}
		if strings.Contains(message, err.Error()) {
			t.Fatal("internal error leaked to client")
		}
	}
	invalid := fmt.Errorf("validate: %w", &provider.MailValidationError{Message: "invalid recipient"})
	if status, _ := composeErrorDetails(invalid); status != http.StatusBadRequest {
		t.Fatal(status)
	}
	for _, err := range []error{service.ErrDraftConflict, service.ErrSubmissionConflict} {
		if status, _ := composeErrorDetails(err); status != http.StatusConflict {
			t.Fatal(status)
		}
	}
}
