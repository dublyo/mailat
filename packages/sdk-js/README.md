# @mailat/sdk

Official JavaScript/TypeScript SDK for [mailat.co](https://mailat.co) API.

## Installation

```bash
npm install @mailat/sdk
# or
pnpm add @mailat/sdk
# or
yarn add @mailat/sdk
```

## Quick Start

```typescript
import { Mailat } from '@mailat/sdk';

const client = new Mailat({
  apiKey: 'ue_your_api_key_here'
});

// Send an email
const result = await client.emails.send({
  from: 'sender@yourdomain.com',
  to: ['recipient@example.com'],
  subject: 'Hello!',
  html: '<p>Welcome to our service!</p>'
});

console.log('Email sent:', result.id);
```

## Configuration

```typescript
const client = new Mailat({
  apiKey: 'ue_your_api_key',      // Required
  baseUrl: 'https://api.mailat.co/api/v1', // Optional, defaults to production
  timeout: 30000,                  // Optional, request timeout in ms
});
```

## Sending Emails

### Single Email

```typescript
const result = await client.emails.send({
  from: 'sender@yourdomain.com',
  to: ['recipient@example.com'],
  cc: ['cc@example.com'],
  bcc: ['bcc@example.com'],
  replyTo: 'reply@yourdomain.com',
  subject: 'Hello {{firstName}}!',
  html: '<p>Welcome, {{firstName}}!</p>',
  text: 'Welcome, {{firstName}}!',
  variables: {
    firstName: 'John'
  },
  tags: ['welcome', 'onboarding'],
  metadata: {
    userId: '12345'
  }
});
```

### With Idempotency Key

```typescript
const result = await client.emails.send(
  {
    from: 'sender@yourdomain.com',
    to: ['recipient@example.com'],
    subject: 'Order Confirmation',
    html: '<p>Your order has been confirmed.</p>'
  },
  {
    idempotencyKey: 'order-123-confirmation'
  }
);
```

### Batch Send

```typescript
const result = await client.emails.sendBatch([
  {
    from: 'sender@yourdomain.com',
    to: ['user1@example.com'],
    subject: 'Hello User 1',
    html: '<p>Message for user 1</p>'
  },
  {
    from: 'sender@yourdomain.com',
    to: ['user2@example.com'],
    subject: 'Hello User 2',
    html: '<p>Message for user 2</p>'
  }
]);

result.results.forEach(r => {
  if (r.error) {
    console.error(`Email ${r.index} failed:`, r.error);
  } else {
    console.log(`Email ${r.index} queued:`, r.id);
  }
});
```

### Get Email Status

```typescript
const status = await client.emails.get('email-uuid');
console.log('Status:', status.status);
console.log('Events:', status.events);
```

### Cancel Scheduled Email

```typescript
await client.emails.cancel('email-uuid');
```

## Templates

### Create Template

```typescript
const template = await client.templates.create({
  name: 'Welcome Email',
  description: 'Sent to new users',
  subject: 'Welcome, {{firstName}}!',
  html: '<h1>Welcome to {{company}}</h1><p>Hello {{firstName}},</p>',
  text: 'Welcome to {{company}}. Hello {{firstName}},'
});
```

### List Templates

```typescript
const templates = await client.templates.list();
```

### Use Template in Email

```typescript
const result = await client.emails.send({
  from: 'sender@yourdomain.com',
  to: ['recipient@example.com'],
  templateId: 'template-uuid',
  variables: {
    firstName: 'John',
    company: 'Acme Inc'
  }
});
```

### Preview Template

```typescript
const preview = await client.templates.preview('template-uuid', {
  firstName: 'John',
  company: 'Acme Inc'
});

console.log('Subject:', preview.subject);
console.log('HTML:', preview.html);
```

## Webhooks

### Create Webhook

```typescript
const webhook = await client.webhooks.create({
  name: 'My Webhook',
  url: 'https://myapp.com/webhooks/email',
  events: ['email.sent', 'email.delivered', 'email.bounced']
});

// Save the secret securely!
console.log('Webhook secret:', webhook.secret);
```

### List Webhooks

```typescript
const webhooks = await client.webhooks.list();
```

### Update Webhook

```typescript
const updated = await client.webhooks.update('webhook-uuid', {
  events: ['email.sent', 'email.delivered', 'email.bounced', 'email.opened']
});
```

### Rotate Secret

```typescript
const newSecret = await client.webhooks.rotateSecret('webhook-uuid');
```

### Get Webhook Calls

```typescript
const calls = await client.webhooks.getCalls('webhook-uuid', 50);
calls.forEach(call => {
  console.log(`${call.eventType}: ${call.status}`);
});
```

## Webhook Verification

### Verify Signature

```typescript
import { Mailat } from '@mailat/sdk';

// In your webhook handler
app.post('/webhooks/email', (req, res) => {
  const signature = req.headers['x-webhook-signature'];
  const payload = req.body; // raw string body

  const isValid = Mailat.verifyWebhookSignature(
    payload,
    signature,
    'whsec_your_secret'
  );

  if (!isValid) {
    return res.status(401).send('Invalid signature');
  }

  // Process the webhook
  const event = JSON.parse(payload);
  console.log('Event type:', event.type);
  console.log('Data:', event.data);

  res.status(200).send('OK');
});
```

### Parse Webhook Payload

```typescript
import { Mailat } from '@mailat/sdk';

// Automatically verifies and parses
const event = Mailat.parseWebhookPayload(
  req.body,
  req.headers['x-webhook-signature'],
  'whsec_your_secret'
);

switch (event.type) {
  case 'email.sent':
    console.log('Email sent:', event.data.email_id);
    break;
  case 'email.delivered':
    console.log('Email delivered:', event.data.email_id);
    break;
  case 'email.bounced':
    console.log('Email bounced:', event.data.email_id, event.data.bounce_type);
    break;
}
```

## Error Handling

```typescript
import { Mailat, MailatError } from '@mailat/sdk';

try {
  await client.emails.send({
    from: 'sender@yourdomain.com',
    to: ['invalid'],
    subject: 'Test',
    html: '<p>Test</p>'
  });
} catch (error) {
  if (error instanceof MailatError) {
    console.error('API Error:', error.message);
    console.error('Status:', error.status);
    console.error('Code:', error.code);
  } else {
    console.error('Unknown error:', error);
  }
}
```

## TypeScript Support

This SDK is written in TypeScript and includes full type definitions:

```typescript
import {
  Mailat,
  SendEmailRequest,
  SendEmailResponse,
  Template,
  Webhook,
  WebhookEvent,
  EmailStatus,
} from '@mailat/sdk';
```

## License

MIT
