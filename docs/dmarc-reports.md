# DMARC Reports organization

Approved October 4, 2026 for every existing and future Mailat user, enabled by
default. This is an application feature; it does not change DNS, root MX,
reporting recipients, domain receiving mode or any other Portainer stack.

## User behavior

**DMARC Reports** is a system folder with its own unread badge. Automatically
organized reports leave the main Inbox and its unread count, but remain visible
in All Mail and search. Messages keep their UUID, attachments, read status,
stars and labels. Users can move reports back to Inbox, archive or trash them.

Settings → Filters & Rules → **Automatically organize DMARC reports** controls
future arrivals for all of the current user's receiving domains. It starts on,
including for users whose settings row has not been created. Turning it off
does not move existing messages or remove the folder. Other settings saves do
not implicitly change this preference.

Reports only arrive when your DMARC record asks for them. Mailat never writes a
reporting address: the policy it can add for you is `v=DMARC1; p=quarantine;`,
which has no `rua=` tag, so no aggregate reports are sent. To fill this folder:

1. Enable receiving on a Mailat domain, so that an address there is delivered to
   a Mailat mailbox (for example `dmarc@example.com`).
2. Add `rua=mailto:<that address>` to the DMARC record of each domain you want
   reports for, for example `v=DMARC1; p=quarantine; rua=mailto:dmarc@example.com`.
3. If the report address is on a different domain than the policy, that domain
   must also publish an authorization record,
   `<policy domain>._report._dmarc.<report domain>` with the TXT value
   `v=DMARC1`, or receivers will not send reports there.

The DMARC panel on the Domains screen shows this reminder whenever no policy
applies or the existing or inherited policy has no `rua=`.

An attachment-only message explains that its content is in the attachment.
This change does not add an XML report dashboard or certify attachment safety.

## Conservative automatic classification

Automatic filing requires trusted SES receipt evidence: DMARC PASS and SPF or
DKIM PASS, with no failed spam or virus verdict. MIME-supplied authentication
headers, sender names, subjects, recipients and filenames do not establish
trust.

Mailat validates an aggregate XML report inside a plain XML, gzip or ZIP
attachment and requires its policy domain to belong to the recipient's
organization. Standard namespace variations are supported. Reports from
multiple providers are handled without a sender-address allowlist.

Replies, forwards, uncertain or missing authentication, foreign report domains,
malformed reports and oversized candidates keep their normal destination.
Parsing failure never rejects the incoming email. Initial SES spam/virus
placement takes precedence over automatic folder/archive rules; explicit Trash
remains allowed. Other explicit user filter destinations override the DMARC
default, and label/read/star filter actions remain available.

Per-message classification bounds:

- 2 MiB compressed candidate data and 10 MiB expanded data.
- 32 ZIP entries, XML depth 64 and 10,000 total report records.
- No nested archives, DTD/entity declarations, external XML references,
  filesystem extraction or attachment execution.

Original stored attachments are unchanged. These limits only determine whether
automatic organization is confident enough to move the message.

## HTTP and SDK contract

The stable folder value is `dmarc-reports`.

| Operation | Contract |
| --- | --- |
| List | `GET /api/v1/inbox/received?folder=dmarc-reports`; normal search, pagination, identity and attachment filters apply. |
| Move | `POST /api/v1/inbox/received/move` with `emailUuids` and `folder`; supports Inbox restore and report-folder moves. |
| Counts | Existing counts plus `inboxUnread`, `dmarcReports`, `dmarcReportsUnread`. |
| Preference read | Human session `GET /api/v1/settings` returns `autoOrganizeDmarcReports`. |
| Preference write | Human session `PUT /api/v1/settings` accepts an optional boolean; explicit `false` disables, omission preserves. |
| Filters | Modern SES filters and compatible `/rules` support the folder destination. |
| Automation | Received events contain the final folder; normal message updates remain in the change feed. |

The existing `unread` count continues to mean all non-trashed unread messages,
including Spam and DMARC Reports. Inbox UI uses `inboxUnread` instead.
API-key permissions and cross-user ownership checks are unchanged. SDKs retain
their existing generic methods and add folder/count/preference types; settings
are not exposed as a newly permitted API-key operation.

## Existing-message migration

Migration 009 records a one-time historical cutoff and durable progress.
Migration 010 adds the default-true preference. The server owns a cancellable
maintenance loop; schema migration itself never fetches S3 objects.

Only authenticated inbound Inbox candidates with attachments, an active owning
user and an enabled preference are considered. Already archived, spam, trashed,
newer or manually modified messages remain in place. Raw storage is private and
read through the existing bounded MIME path.

Each batch handles at most 25 candidates with a two-minute deadline and
30-second object-read limits. A session lock prevents API replicas from
duplicating the scan. Storage failures retry up to three times across restarts;
an unavailable source then stays in place with a generic diagnostic, allowing
later messages to proceed. Database lookup failures retain progress for retry.

The final preference/version check, move and progress update share a transaction
and the same per-user lock as settings and mailbox changes. An opt-out, read,
archive or manual Inbox restore committed while inspection runs is respected.
Moved messages generate ordinary change-feed updates, never another
`email.received` webhook or send. A completed scan never runs again and never
undoes a later manual Inbox move.

## Verification and release

Local acceptance covers parser formats and limits, trusted verdicts, false
positives, per-user routing, filter precedence, final event folders, cutoff and
retry behavior, concurrent user changes, account isolation, folder APIs and
counts, settings omission/false semantics and byte-identical private downloads.

Native desktop and 390-pixel mobile checks verified the separate folder,
attachment-only detail, unread badge behavior, Inbox restore, move back to
Reports, All Mail/search, default-on setting, opt-out persistence after reload
and re-enabling. Only synthetic local accounts and mocked external storage were
used. Production mail has not been moved by this development phase.

Final local results on October 4, 2026:

| Check | Result |
| --- | --- |
| API `go test -race -count=1 ./...`, isolated PostgreSQL | 95 top-level tests and 163 subcases passed across 10 test packages; zero failures. |
| Optional n8n acceptance fixture | Intentionally skipped in the default suite; executed successfully during the earlier automation phase and not rerun for folder changes. |
| Frontend full suite | 64 passed; zero failures/skips. |
| Frontend production build | Vue typecheck and Vite build passed. |
| API production build | Linux amd64, CGO disabled, passed. |
| JavaScript SDK | 3 tests, typecheck and package builds passed. |
| Python SDK | 3 tests passed. |
| Go SDK | 3 tests passed with race detection. |
| OpenAPI | Folder/count/optional-boolean and authorization contracts passed; generated document matches 180 paths. |
| Native browser | Desktop and mobile checks described above passed against the real local API fixture. |
| Patch whitespace | `git diff --check` passed. |

Acceptance also verified legacy `/rules` destination synchronization, SSE final
folder serialization and per-user delivery, private attachment ownership/byte
fidelity, and that unrelated settings saves cannot change the DMARC preference.
Parser and historical sorting used synthetic XML/ZIP/gzip reports and mocked
storage, not the contents of any production user's mailbox.

Evidence is retained in the workspace's private `.mailat-backups/` directory:
`dmarc-folder-go-tests.jsonl`, `dmarc-folder-web-tests.txt`, and
`dmarc-folder-web-build.txt`. The temporary browser account was signed out and
its tab closed; the local API fixture and Vite server were stopped.

A GitHub push and Mailat-only Portainer deployment remain separate approved
release steps. Before release, retain application/database rollback evidence;
older binaries do not expose the new system folder. No production mail, DNS,
AWS resources or Portainer stacks were modified during this implementation.
