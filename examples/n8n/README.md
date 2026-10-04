# Mailat n8n examples

These importable workflows use standard n8n nodes and Mailat's public UUIDs. They target self-hosted n8n with JavaScript Code nodes and PostgreSQL credentials. No community nodes are required.

- `incoming-mail.json`: verify a signed incoming event, claim a durable receipt, fetch the message and every attachment, reply using its owned alias/thread context, label it, and archive it. Reading the message does not mark it read.
- `send-and-batch.json`: deliberately send one message with an attachment, submit a three-item partial batch, and read delivery status. The third batch item intentionally has an invalid address to demonstrate per-item failure.
- `state.sql`: persistent incoming-event deduplication in your automation database.
- `acceptance.py`: local runtime acceptance against Mailat's opt-in service fixture, with fake SES and storage.

## Configure

1. Create a Mailat API key for the mailbox owner. Incoming automation needs `email:read`, `email:send`, and `email:manage`; the send example needs `email:read` and `email:send`. Set up sending resources and verify the sending domain before using attachments. Receiving setup is separate.
2. Create an n8n **Header Auth** credential named **Mailat API token** with header `Authorization` and value `Bearer YOUR_API_KEY`. Select it on every HTTP Request node after import. Workflow JSON contains credential references, never API keys.
3. Run `state.sql` in a dedicated automation PostgreSQL database and create an n8n **Postgres** credential named **Mailat automation state** for that database. Select it on both Postgres nodes. Do not use the Mailat application database for this state.
4. Create the Mailat label `Automated` (or use the name in `MAILAT_LABEL`). Import the workflows, customize the acknowledgement and choose which received mail should trigger it. Only publish the incoming workflow after configuration.
5. Configure these environment variables in n8n and its JavaScript runner:

   | Variable | Value |
   | --- | --- |
   | `MAILAT_API_URL` | `https://mail.example.com/api/v1` without a trailing slash |
   | `MAILAT_WEBHOOK_SECRET` | The secret returned when creating the Mailat webhook |
   | `MAILAT_LABEL` | Optional label name; defaults to `Automated` |
   | `NODE_FUNCTION_ALLOW_BUILTIN` | `crypto` |
   | `N8N_BLOCK_ENV_ACCESS_IN_NODE` | `false` |

   Environment access assumes a trusted, dedicated automation instance. Keep the secret out of workflow exports. Use external runners for production and configure the allowed built-in and environment values there; the local acceptance harness uses n8n's internal JavaScript runner. n8n Cloud is not covered by this configuration.
6. Create a Mailat webhook subscribed to `email.received`, targeting the published n8n production URL ending in `/webhook/mailat-incoming`. Mailat requires a public HTTPS destination. Use the returned secret above. Keep **Raw Body** enabled on the Webhook node.

The send example also requires `MAILAT_FROM`, `MAILAT_TO`, `MAILAT_SEND_KEY`, and `MAILAT_BATCH_KEY`. Running it sends three valid messages to `MAILAT_TO`. Use keys representing a stable business operation (8–128 characters), and preserve them across retries. For a different operation, deliberately provide different keys. In a larger workflow, replace the manual input Code node with your business request and persistent keys.

## Receipt and failure behavior

The incoming workflow verifies HMAC-SHA256 over `timestamp + "." + exact raw body`, checks a five-minute timestamp window, checks the envelope version/type and public UUIDs, and matches `X-Webhook-ID`. Tampered or expired signatures return 401 before any database claim or Mailat operation.

The PostgreSQL primary key permits one event claim. A completed duplicate returns 200. An active claim returns 503 so Mailat can retry. A failed execution can reclaim its receipt after ten minutes; the workflow has a four-minute timeout. The stable `n8n-reply:<event UUID>` submission key makes recovery safe even if the reply succeeded before a later label/archive step failed. Label and archive operations are repeatable. The receipt is completed only after those operations succeed. Keep receipts for at least as long as you permit webhook replay.

Reply context supplies the identity, envelope alias, Reply-To recipient, thread headers, and inline attachment references. The workflow maps context `from.email` to send-request `fromEmail`. The attachment branch downloads every private attachment and demonstrates byte access; replace **Inspect downloaded bytes** with your processing/storage step if needed. It does not store downloaded files permanently or attach unrelated files to the reply.

The send example classifies each batch result independently and retains validation failures without inventing a UUID. `queued`/`sending` means poll the same UUID later. `sent` means provider acceptance, not inbox placement; delivery/bounce/complaint events can update that status later. `unknown` means inspect the existing receipt and never automatically create a new key. HTTP/network failures should keep the original request and key. Changed content with the same key returns a conflict. A deliberate corrected request requires a new key.

For missed incoming events, use `/inbox/changes` with a durable cursor. Store the returned cursor only after processing every page's changes; handle deletion tombstones and the expired-cursor response with a fresh mailbox snapshot. Cursor recovery is documented here but is not a third imported workflow.

## Reproduce local acceptance

Use a disposable local PostgreSQL cluster, an empty database named `mailat_n8n_acceptance`, Node 24, Python 3, and `psql`. Do not point these commands at a production database or n8n installation. Replace the example paths and local connection values with your disposable resources.

```sh
rtk proxy npm install --prefix /tmp/mailat-n8n-test n8n@2.41.6 --no-audit --no-fund
```

From `apps/api`, keep this fixture running in one terminal. It creates and drops an isolated Mailat schema, binds a loopback HTTP server, and uses mocked provider/storage implementations:

```sh
rtk proxy env \
  MAILAT_TEST_DATABASE_URL='postgres://TEST_USER@127.0.0.1:TEST_PORT/postgres?sslmode=disable' \
  MAILAT_N8N_STATE=/tmp/mailat-n8n-test/fixture.json \
  go test -race -timeout 35m -count=1 ./internal/service -run '^TestN8NAcceptanceFixture$' -v
```

After `fixture.json` is written, run from the repository root:

```sh
rtk proxy python3 examples/n8n/acceptance.py \
  --runtime /tmp/mailat-n8n-test \
  --fixture /tmp/mailat-n8n-test/fixture.json \
  --state-url 'postgres://TEST_USER@127.0.0.1:TEST_PORT/mailat_n8n_acceptance?sslmode=disable' \
  --psql /path/to/psql
```

The harness imports credentials and workflows into its private n8n user folder, publishes the actual incoming webhook, tests signature failures and replay across an n8n restart, executes the send/batch workflow twice via n8n CLI, and verifies stable receipts with exactly four mocked provider calls. It writes `acceptance-results.json`, stops its n8n process, deletes its temporary credential import file, and asks the Go fixture to finish. Remove the dedicated n8n database and private runtime afterward. If the harness fails, its logs remain available; the fixture times out after 30 minutes.

This service-backed fixture checks real Mailat database/service behavior through test HTTP adapters. Production controllers, API-key middleware, SNS verification, and public webhook dispatch are covered separately by Go tests; the n8n acceptance does not send real email or configure AWS. PostgreSQL 16 worked for this acceptance; n8n 2.41.6 reports compatibility-only support for it and recommends PostgreSQL 17 or newer.

## Official references

- [Webhook raw body and response options](https://github.com/n8n-io/n8n-docs/blob/main/docs/integrations/builtin/core-nodes/n8n-nodes-base.webhook/README.md)
- [Binary data access in Code nodes](https://github.com/n8n-io/n8n-docs/blob/main/docs/build/code-in-n8n/cookbook/code-node/get-the-binary-data-buffer.md)
- [n8n CLI workflow import and execution](https://github.com/n8n-io/n8n-docs/blob/main/docs/deploy/host-n8n/configure-n8n/use-the-command-line.md)
- [Task runner configuration](https://github.com/n8n-io/n8n-docs/blob/main/docs/deploy/host-n8n/configure-n8n/set-up-task-runners.md)

Workflow static data is intentionally not used as the event receipt: it is unsuitable as an atomic, concurrent deduplication store. The Postgres claim is shared across n8n processes and survives restarts.
