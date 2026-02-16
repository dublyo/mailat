# mailat.co Go SDK

Official Go SDK for the mailat.co transactional email API.

## Installation

```bash
go get github.com/dublyo/mailat-go
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/dublyo/mailat-go/mailat"
)

func main() {
    client := mailat.NewClient("ue_your_api_key")

    // Send an email
    resp, err := client.Emails.Send(context.Background(), &mailat.SendEmailRequest{
        From:    "sender@yourdomain.com",
        To:      []string{"recipient@example.com"},
        Subject: "Hello from mailat.co!",
        HTML:    "<p>Welcome to our service!</p>",
    }, nil)
    if err != nil {
        log.Fatal(err)
    }

    fmt.Printf("Email sent! ID: %s\n", resp.ID)
}
```

## Configuration

```go
// Custom base URL (for self-hosted)
client := mailat.NewClient("ue_key", mailat.WithBaseURL("https://your-instance.com/api/v1"))

// Custom timeout
client := mailat.NewClient("ue_key", mailat.WithTimeout(60 * time.Second))

// Custom HTTP client
httpClient := &http.Client{Transport: customTransport}
client := mailat.NewClient("ue_key", mailat.WithHTTPClient(httpClient))
```

## Emails

### Send Single Email

```go
resp, err := client.Emails.Send(ctx, &mailat.SendEmailRequest{
    From:    "sender@yourdomain.com",
    To:      []string{"recipient@example.com"},
    Subject: "Hello!",
    HTML:    "<p>Welcome!</p>",
    Text:    "Welcome!",
    CC:      []string{"cc@example.com"},
    BCC:     []string{"bcc@example.com"},
    ReplyTo: "reply@yourdomain.com",
    Tags:    []string{"welcome", "onboarding"},
    Metadata: map[string]string{
        "user_id": "123",
    },
}, &mailat.SendOptions{
    IdempotencyKey: "unique-key-123",
})
```

### Send with Template

```go
resp, err := client.Emails.Send(ctx, &mailat.SendEmailRequest{
    From:       "sender@yourdomain.com",
    To:         []string{"recipient@example.com"},
    Subject:    "Welcome!",
    TemplateID: "template-uuid",
    Variables: map[string]string{
        "name":    "John",
        "company": "Acme Inc",
    },
}, nil)
```

### Batch Send

```go
emails := []mailat.SendEmailRequest{
    {
        From:    "sender@yourdomain.com",
        To:      []string{"user1@example.com"},
        Subject: "Hello User 1",
        HTML:    "<p>Hello!</p>",
    },
    {
        From:    "sender@yourdomain.com",
        To:      []string{"user2@example.com"},
        Subject: "Hello User 2",
        HTML:    "<p>Hello!</p>",
    },
}

resp, err := client.Emails.SendBatch(ctx, emails)
fmt.Printf("Sent: %d, Failed: %d\n", resp.Sent, resp.Failed)
```

### Get Email Status

```go
status, err := client.Emails.Get(ctx, "email-uuid")
fmt.Printf("Status: %s\n", status.Status)
for _, event := range status.Events {
    fmt.Printf("  %s: %s\n", event.Event, event.Timestamp)
}
```

### Cancel Scheduled Email

```go
err := client.Emails.Cancel(ctx, "email-uuid")
```

## Templates

### Create Template

```go
template, err := client.Templates.Create(ctx, &mailat.CreateTemplateRequest{
    Name:        "Welcome Email",
    Subject:     "Welcome, {{name}}!",
    HTML:        "<h1>Welcome, {{name}}!</h1><p>Thanks for joining {{company}}.</p>",
    Text:        "Welcome, {{name}}! Thanks for joining {{company}}.",
    Description: "Sent to new users after signup",
})
```

### List Templates

```go
templates, err := client.Templates.List(ctx)
for _, t := range templates {
    fmt.Printf("%s: %s\n", t.ID, t.Name)
}
```

### Update Template

```go
active := false
template, err := client.Templates.Update(ctx, "template-uuid", &mailat.UpdateTemplateRequest{
    IsActive: &active,
})
```

### Preview Template

```go
preview, err := client.Templates.Preview(ctx, "template-uuid", map[string]string{
    "name":    "John",
    "company": "Acme Inc",
})
fmt.Println(preview.HTML)
```

### Delete Template

```go
err := client.Templates.Delete(ctx, "template-uuid")
```

## Webhooks

### Create Webhook

```go
webhook, err := client.Webhooks.Create(ctx, &mailat.CreateWebhookRequest{
    Name:   "Delivery Events",
    URL:    "https://yourapp.com/webhooks/email",
    Events: []string{"email.delivered", "email.bounced", "email.complained"},
})
fmt.Printf("Secret: %s\n", webhook.Secret) // Store this securely!
```

### List Webhooks

```go
webhooks, err := client.Webhooks.List(ctx)
```

### Update Webhook

```go
active := false
webhook, err := client.Webhooks.Update(ctx, "webhook-uuid", &mailat.UpdateWebhookRequest{
    Active: &active,
})
```

### Rotate Secret

```go
newSecret, err := client.Webhooks.RotateSecret(ctx, "webhook-uuid")
```

### Get Delivery History

```go
calls, err := client.Webhooks.GetCalls(ctx, "webhook-uuid", 50)
for _, call := range calls {
    fmt.Printf("%s: %d %v\n", call.Event, call.StatusCode, call.Success)
}
```

### Test Webhook

```go
err := client.Webhooks.Test(ctx, "webhook-uuid")
```

## Webhook Verification

Verify incoming webhooks in your HTTP handler:

```go
import (
    "io"
    "net/http"
    "time"

    "github.com/dublyo/mailat-go/mailat"
)

func webhookHandler(w http.ResponseWriter, r *http.Request) {
    payload, _ := io.ReadAll(r.Body)
    signature := r.Header.Get("X-Webhook-Signature")
    secret := "whsec_your_webhook_secret"

    // Verify and parse
    event, err := mailat.ParseWebhookPayload(payload, signature, secret)
    if err != nil {
        http.Error(w, "Invalid signature", http.StatusUnauthorized)
        return
    }

    // Handle the event
    switch event.Event {
    case "email.delivered":
        emailID := event.Data["emailId"].(string)
        fmt.Printf("Email delivered: %s\n", emailID)
    case "email.bounced":
        emailID := event.Data["emailId"].(string)
        reason := event.Data["reason"].(string)
        fmt.Printf("Email bounced: %s - %s\n", emailID, reason)
    }

    w.WriteHeader(http.StatusOK)
}
```

Or verify manually:

```go
valid := mailat.VerifyWebhookSignature(
    payload,
    signature,
    secret,
    5 * time.Minute, // Tolerance
)
```

## Error Handling

```go
resp, err := client.Emails.Send(ctx, req, nil)
if err != nil {
    if apiErr, ok := err.(*mailat.APIError); ok {
        fmt.Printf("API Error: %s (status: %d, code: %s)\n",
            apiErr.Message, apiErr.StatusCode, apiErr.Code)
    } else {
        fmt.Printf("Error: %v\n", err)
    }
    return
}
```

## Supported Events

- `email.sent` - Email accepted for delivery
- `email.delivered` - Email delivered to recipient
- `email.bounced` - Email bounced (hard or soft)
- `email.complained` - Recipient marked as spam
- `email.opened` - Email opened (if tracking enabled)
- `email.clicked` - Link clicked (if tracking enabled)
- `email.failed` - Delivery failed

## License

MIT License
