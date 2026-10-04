"""Main client for mailat.co SDK."""

import hashlib
import hmac
import time
import re
import math
from urllib.parse import quote
from mailat.core import Inbox, Compose, Domains, Identities, Triggers, Deliveries, send_headers
from typing import Any, Callable, Dict, List, Optional, Union

import httpx

from mailat.models import (
    Attachment,
    BatchSendResponse,
    CreateTemplateRequest,
    CreateWebhookRequest,
    EmailStatusResponse,
    PreviewTemplateResponse,
    SendEmailRequest,
    SendEmailResponse,
    Template,
    MailatError,
    UpdateTemplateRequest,
    UpdateWebhookRequest,
    Webhook,
    WebhookCall,
    WebhookPayload,
)


DEFAULT_BASE_URL = "https://api.mailat.co/api/v1"
DEFAULT_TIMEOUT = 30.0


class Emails:
    """Email sending operations."""

    def __init__(self, client: "Mailat") -> None:
        self._client = client

    def send(
        self,
        from_address: str,
        to: List[str],
        subject: str,
        html: Optional[str] = None,
        text: Optional[str] = None,
        cc: Optional[List[str]] = None,
        bcc: Optional[List[str]] = None,
        reply_to: Optional[str] = None,
        template_id: Optional[str] = None,
        variables: Optional[Dict[str, str]] = None,
        tags: Optional[List[str]] = None,
        metadata: Optional[Dict[str, str]] = None,
        scheduled_for: Optional[str] = None,
        idempotency_key: Optional[str] = None,
        attachments: Optional[List[Attachment]] = None,
    ) -> SendEmailResponse:
        """
        Send a single transactional email.

        Args:
            from_address: Sender email address
            to: List of recipient email addresses
            subject: Email subject
            html: HTML body content
            text: Plain text body content
            cc: CC recipients
            bcc: BCC recipients
            reply_to: Reply-to address
            template_id: Template UUID to use
            variables: Template variables
            tags: Tags for categorization
            metadata: Custom metadata
            scheduled_for: RFC3339 timestamp for scheduled sending
            idempotency_key: Unique key for idempotent requests

        Returns:
            SendEmailResponse with email ID and status
        """
        data = {
            "from": from_address,
            "to": to,
            "subject": subject,
        }
        if html:
            data["html"] = html
        if text:
            data["text"] = text
        if cc:
            data["cc"] = cc
        if bcc:
            data["bcc"] = bcc
        if reply_to:
            data["replyTo"] = reply_to
        if template_id:
            data["templateId"] = template_id
        if variables:
            data["variables"] = variables
        if tags:
            data["tags"] = tags
        if metadata:
            data["metadata"] = metadata
        if scheduled_for:
            data["scheduledFor"] = scheduled_for

        if attachments is not None:
            data["attachments"] = [a.model_dump(by_alias=True, exclude_none=True) for a in attachments]
        headers = send_headers(idempotency_key)

        response = self._client._request("POST", "/emails", data, headers)
        return SendEmailResponse(**response["data"])

    def send_batch(self, emails: List[SendEmailRequest], idempotency_key: Optional[str] = None) -> BatchSendResponse:
        """
        Send multiple emails in a batch (up to 100).

        Args:
            emails: List of email requests

        Returns:
            BatchSendResponse with results for each email
        """
        if not emails or len(emails) > 100:
            raise ValueError("Batch size cannot exceed 100 emails")

        data = {"emails": [e.model_dump(by_alias=True, exclude_none=True) for e in emails]}
        response = self._client._request("POST", "/emails/batch", data, send_headers(idempotency_key))
        return BatchSendResponse(**response["data"])

    def get(self, email_id: str) -> EmailStatusResponse:
        """
        Get email status and delivery events.

        Args:
            email_id: Email UUID

        Returns:
            EmailStatusResponse with status and events
        """
        response = self._client._request("GET", f"/emails/{quote(email_id, safe='')}")
        return EmailStatusResponse(**response["data"])

    def cancel(self, email_id: str) -> None:
        """
        Cancel a scheduled email.

        Args:
            email_id: Email UUID

        Raises:
            MailatError if email is not in queued status
        """
        self._client._request("DELETE", f"/emails/{quote(email_id, safe='')}")


class Templates:
    """Template management operations."""

    def __init__(self, client: "Mailat") -> None:
        self._client = client

    def create(
        self,
        name: str,
        subject: str,
        html: str,
        text: Optional[str] = None,
        description: Optional[str] = None,
    ) -> Template:
        """Create a new email template."""
        data = {
            "name": name,
            "subject": subject,
            "html": html,
        }
        if text:
            data["text"] = text
        if description:
            data["description"] = description

        response = self._client._request("POST", "/templates", data)
        return Template(**response["data"])

    def get(self, uuid: str) -> Template:
        """Get a template by UUID."""
        response = self._client._request("GET", f"/templates/{quote(uuid, safe='')}")
        return Template(**response["data"])

    def list(self) -> List[Template]:
        """List all templates."""
        response = self._client._request("GET", "/templates")
        return [Template(**t) for t in (response["data"] or [])]

    def update(
        self,
        uuid: str,
        name: Optional[str] = None,
        subject: Optional[str] = None,
        html: Optional[str] = None,
        text: Optional[str] = None,
        description: Optional[str] = None,
        is_active: Optional[bool] = None,
    ) -> Template:
        """Update a template."""
        data: Dict[str, Any] = {}
        if name:
            data["name"] = name
        if subject:
            data["subject"] = subject
        if html:
            data["html"] = html
        if text:
            data["text"] = text
        if description:
            data["description"] = description
        if is_active is not None:
            data["isActive"] = is_active

        response = self._client._request("PUT", f"/templates/{quote(uuid, safe='')}", data)
        return Template(**response["data"])

    def delete(self, uuid: str) -> None:
        """Delete a template."""
        self._client._request("DELETE", f"/templates/{quote(uuid, safe='')}")

    def preview(
        self, uuid: str, variables: Optional[Dict[str, str]] = None
    ) -> PreviewTemplateResponse:
        """Preview a template with variables."""
        data = {"variables": variables or {}}
        response = self._client._request("POST", f"/templates/{quote(uuid, safe='')}/preview", data)
        return PreviewTemplateResponse(**response["data"])


class Webhooks:
    """Webhook management operations."""

    def __init__(self, client: "Mailat") -> None:
        self._client = client

    def create(self, name: str, url: str, events: List[str]) -> Webhook:
        """Create a new webhook endpoint."""
        data = {
            "name": name,
            "url": url,
            "events": events,
        }
        response = self._client._request("POST", "/webhooks", data)
        return Webhook(**response["data"])

    def get(self, uuid: str) -> Webhook:
        """Get a webhook by UUID."""
        response = self._client._request("GET", f"/webhooks/{quote(uuid, safe='')}")
        return Webhook(**response["data"])

    def list(self) -> List[Webhook]:
        """List all webhooks."""
        response = self._client._request("GET", "/webhooks")
        return [Webhook(**w) for w in (response["data"] or [])]

    def update(
        self,
        uuid: str,
        name: Optional[str] = None,
        url: Optional[str] = None,
        events: Optional[List[str]] = None,
        active: Optional[bool] = None,
    ) -> Webhook:
        """Update a webhook."""
        data: Dict[str, Any] = {}
        if name:
            data["name"] = name
        if url:
            data["url"] = url
        if events:
            data["events"] = events
        if active is not None:
            data["active"] = active

        response = self._client._request("PUT", f"/webhooks/{quote(uuid, safe='')}", data)
        return Webhook(**response["data"])

    def delete(self, uuid: str) -> None:
        """Delete a webhook."""
        self._client._request("DELETE", f"/webhooks/{quote(uuid, safe='')}")

    def rotate_secret(self, uuid: str) -> str:
        """Rotate the webhook secret."""
        response = self._client._request("POST", f"/webhooks/{quote(uuid, safe='')}/rotate-secret")
        return response["data"]["secret"]

    def get_calls(self, uuid: str, limit: int = 50) -> List[WebhookCall]:
        """Get recent webhook delivery attempts."""
        response = self._client._request("GET", f"/webhooks/{quote(uuid, safe='')}/calls?limit={limit}")
        return [WebhookCall(**c) for c in (response["data"] or [])]

    def test(self, uuid: str) -> Dict[str, Any]:
        """Return the actual delivery status, including receiver failure."""
        return self._client._request("POST", f"/webhooks/{quote(uuid, safe='')}/test")["data"]


class Mailat:
    """
    mailat.co API client.

    Example:
        >>> client = Mailat(api_key="ue_your_api_key")
        >>> result = client.emails.send(
        ...     from_address="sender@yourdomain.com",
        ...     to=["recipient@example.com"],
        ...     subject="Hello!",
        ...     html="<p>Welcome!</p>", idempotency_key="welcome-123"
        ... )
    """

    def __init__(
        self,
        api_key: str,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = DEFAULT_TIMEOUT,
    ) -> None:
        """
        Initialize the client.

        Args:
            api_key: Your API key (starts with 'ue_')
            base_url: API base URL (defaults to production)
            timeout: Request timeout in seconds
        """
        if not api_key:
            raise ValueError("API key is required")

        self._api_key = api_key
        self._base_url = base_url.rstrip("/")
        self._timeout = timeout
        self._client = httpx.Client(
            timeout=timeout,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
                "User-Agent": "mailat-python/0.1.0",
            },
        )

        # Resource namespaces
        self.emails = Emails(self)
        self.templates = Templates(self)
        self.webhooks = Webhooks(self)
        self.inbox = Inbox(self)
        self.compose = Compose(self)
        self.domains = Domains(self)
        self.identities = Identities(self)
        self.triggers = Triggers(self)
        self.deliveries = Deliveries(self)

    def _request(
        self,
        method: str,
        path: str,
        data: Optional[Dict[str, Any]] = None,
        headers: Optional[Dict[str, str]] = None,
        binary: bool = False,
    ) -> Any:
        """Make an API request."""
        url = f"{self._base_url}{path}"

        try:
            response = self._client.request(
                method,
                url,
                json=data,
                headers=headers,
            )
            if response.is_success and binary:
                return response.content
            try:
                result = response.json() if response.content else {}
            except ValueError:
                raise MailatError("Unexpected non-JSON API response", response.status_code)

            if not response.is_success:
                raise MailatError(
                    result.get("message", "Request failed"),
                    response.status_code,
                    result.get("code"),
                    response.headers.get("Retry-After"),
                )

            return result

        except httpx.TimeoutException:
            raise MailatError("Request timeout; keep the same send key when retrying", 408)
        except httpx.RequestError as e:
            raise MailatError(str(e), 0)

    def close(self) -> None:
        """Close the HTTP client."""
        self._client.close()

    def __enter__(self) -> "Mailat":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    @staticmethod
    def verify_webhook_signature(
        payload: Union[str, bytes],
        signature: str,
        secret: str,
        tolerance: int = 300,
    ) -> bool:
        """
        Verify a webhook signature.

        Args:
            payload: The raw request body
            signature: The X-Webhook-Signature header value
            secret: Your webhook secret
            tolerance: Maximum age of the webhook in seconds (default: 5 minutes)

        Returns:
            True if the signature is valid
        """
        if not secret or not math.isfinite(tolerance) or tolerance < 0:
            return False
        fields = {}
        for part in signature.split(","):
            match = re.fullmatch(r"(t|v1)=([^=]+)", part.strip())
            if not match or match[1] in fields:
                return False
            fields[match[1]] = match[2]
        timestamp = fields.get("t", "")
        digest = fields.get("v1", "")
        if not re.fullmatch(r"[0-9]{1,13}", timestamp) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            return False
        if int(timestamp) <= 0 or abs(time.time() - int(timestamp)) > tolerance:
            return False
        raw = payload.encode("utf-8") if isinstance(payload, str) else payload
        expected = hmac.new(secret.encode("utf-8"), timestamp.encode("ascii") + b"." + raw, hashlib.sha256).hexdigest()
        return hmac.compare_digest(digest, expected)

    @staticmethod
    def parse_webhook_payload(
        payload: Union[str, bytes],
        signature: str,
        secret: str,
        claim_event: Optional[Callable[[str], bool]] = None,
    ) -> WebhookPayload:
        """
        Verify and parse a webhook payload.

        Args:
            payload: The raw request body
            signature: The X-Webhook-Signature header value
            secret: Your webhook secret

        Returns:
            Parsed WebhookPayload

        Raises:
            MailatError if signature verification fails
        """
        if not Mailat.verify_webhook_signature(payload, signature, secret):
            raise MailatError("Invalid webhook signature", 401)

        if isinstance(payload, bytes):
            payload = payload.decode("utf-8")

        import json
        try:
            event = WebhookPayload(**json.loads(payload))
        except (ValueError, TypeError):
            raise MailatError("Invalid webhook event envelope", 400)
        if event.version != "1" or not event.id or not event.type:
            raise MailatError("Invalid webhook event envelope", 400)
        # Consumers can atomically claim durable IDs before processing retries.
        if claim_event is not None and not claim_event(event.id):
            raise MailatError("Webhook event already processed", 409)
        return event
