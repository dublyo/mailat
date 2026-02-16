"""
Emails resource for sending and managing transactional emails
"""

from typing import TYPE_CHECKING, Any, Optional
from datetime import datetime
import base64

from ..types import Email, EmailAddress, SendEmailOptions, EmailEvent

if TYPE_CHECKING:
    from ..client import Mailat


class EmailsResource:
    """
    Resource for sending and managing transactional emails.

    Example:
        ```python
        # Send a simple email
        email = client.emails.send(
            to="recipient@example.com",
            subject="Hello World",
            html="<h1>Hello!</h1>"
        )

        # Send with template
        email = client.emails.send_with_template(
            template_id="welcome-email",
            to="user@example.com",
            template_data={"name": "John"}
        )
        ```
    """

    def __init__(self, client: "Mailat"):
        self._client = client

    def send(
        self,
        to: str | list[str] | EmailAddress | list[EmailAddress],
        subject: str,
        html: Optional[str] = None,
        text: Optional[str] = None,
        cc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None,
        bcc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None,
        from_address: Optional[str | EmailAddress] = None,
        reply_to: Optional[str | EmailAddress] = None,
        template_id: Optional[str] = None,
        template_data: Optional[dict[str, Any]] = None,
        attachments: Optional[list[dict[str, Any]]] = None,
        tags: Optional[list[str]] = None,
        metadata: Optional[dict[str, Any]] = None,
        headers: Optional[dict[str, str]] = None,
        scheduled_at: Optional[datetime] = None,
        idempotency_key: Optional[str] = None,
    ) -> Email:
        """
        Send a transactional email.

        Args:
            to: Recipient email(s)
            subject: Email subject
            html: HTML content
            text: Plain text content
            cc: CC recipients
            bcc: BCC recipients
            from_address: Sender address
            reply_to: Reply-to address
            template_id: Template ID to use
            template_data: Variables for template
            attachments: File attachments
            tags: Tags for categorization
            metadata: Custom metadata
            headers: Custom headers
            scheduled_at: Schedule for future sending
            idempotency_key: Idempotency key

        Returns:
            Created Email object
        """
        payload = self._build_payload(
            to=to,
            subject=subject,
            html=html,
            text=text,
            cc=cc,
            bcc=bcc,
            from_address=from_address,
            reply_to=reply_to,
            template_id=template_id,
            template_data=template_data,
            attachments=attachments,
            tags=tags,
            metadata=metadata,
            headers=headers,
            scheduled_at=scheduled_at,
            idempotency_key=idempotency_key,
        )

        data = self._client.post("/emails", json=payload)
        return Email(**data)

    def send_batch(
        self,
        emails: list[SendEmailOptions],
    ) -> dict[str, Any]:
        """
        Send multiple emails in batch.

        Args:
            emails: List of email options

        Returns:
            Dict with sent and failed counts
        """
        payloads = [
            self._build_payload(
                to=e.to,
                subject=e.subject,
                html=e.html,
                text=e.text,
                cc=e.cc,
                bcc=e.bcc,
                template_id=e.template_id,
                template_data=e.template_data,
                tags=e.tags,
                metadata=e.metadata,
            )
            for e in emails
        ]

        return self._client.post("/emails/batch", json={"emails": payloads})

    def get(self, email_id: str) -> Email:
        """Get email by ID."""
        data = self._client.get(f"/emails/{email_id}")
        return Email(**data)

    def cancel(self, email_id: str) -> Email:
        """Cancel a scheduled email."""
        data = self._client.delete(f"/emails/{email_id}")
        return Email(**data)

    def list(
        self,
        page: int = 1,
        limit: int = 50,
        status: Optional[str] = None,
        tag: Optional[str] = None,
    ) -> dict[str, Any]:
        """List recent emails."""
        params = {"page": page, "limit": limit}
        if status:
            params["status"] = status
        if tag:
            params["tag"] = tag

        return self._client.get("/emails", params=params)

    def get_events(self, email_id: str) -> list[EmailEvent]:
        """Get email delivery events."""
        data = self._client.get(f"/emails/{email_id}/events")
        return [EmailEvent(**e) for e in data]

    def send_with_template(
        self,
        template_id: str,
        to: str | list[str] | EmailAddress | list[EmailAddress],
        template_data: Optional[dict[str, Any]] = None,
        cc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None,
        bcc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None,
        tags: Optional[list[str]] = None,
        metadata: Optional[dict[str, Any]] = None,
    ) -> Email:
        """Send email using a template."""
        return self.send(
            to=to,
            subject="",  # Will come from template
            template_id=template_id,
            template_data=template_data,
            cc=cc,
            bcc=bcc,
            tags=tags,
            metadata=metadata,
        )

    def _build_payload(
        self,
        to: str | list[str] | EmailAddress | list[EmailAddress],
        subject: str,
        html: Optional[str] = None,
        text: Optional[str] = None,
        cc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None,
        bcc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None,
        from_address: Optional[str | EmailAddress] = None,
        reply_to: Optional[str | EmailAddress] = None,
        template_id: Optional[str] = None,
        template_data: Optional[dict[str, Any]] = None,
        attachments: Optional[list[dict[str, Any]]] = None,
        tags: Optional[list[str]] = None,
        metadata: Optional[dict[str, Any]] = None,
        headers: Optional[dict[str, str]] = None,
        scheduled_at: Optional[datetime] = None,
        idempotency_key: Optional[str] = None,
    ) -> dict[str, Any]:
        """Build request payload."""
        payload: dict[str, Any] = {
            "to": self._normalize_addresses(to),
            "subject": subject,
        }

        if html:
            payload["htmlBody"] = html
        if text:
            payload["textBody"] = text
        if cc:
            payload["cc"] = self._normalize_addresses(cc)
        if bcc:
            payload["bcc"] = self._normalize_addresses(bcc)
        if from_address:
            payload["from"] = self._normalize_address(from_address)
        if reply_to:
            payload["replyTo"] = self._normalize_address(reply_to)
        if template_id:
            payload["templateId"] = template_id
        if template_data:
            payload["variables"] = template_data
        if attachments:
            payload["attachments"] = [
                {
                    "filename": a["filename"],
                    "content": (
                        base64.b64encode(a["content"]).decode()
                        if isinstance(a["content"], bytes)
                        else a["content"]
                    ),
                    "contentType": a.get("content_type"),
                    "contentId": a.get("content_id"),
                }
                for a in attachments
            ]
        if tags:
            payload["tags"] = tags
        if metadata:
            payload["metadata"] = metadata
        if headers:
            payload["headers"] = headers
        if scheduled_at:
            payload["scheduledFor"] = scheduled_at.isoformat()
        if idempotency_key:
            payload["idempotencyKey"] = idempotency_key

        return payload

    def _normalize_addresses(
        self,
        addresses: str | list[str] | EmailAddress | list[EmailAddress],
    ) -> list[dict[str, str]]:
        """Normalize addresses to list of dicts."""
        if isinstance(addresses, str):
            return [{"email": addresses}]
        if isinstance(addresses, EmailAddress):
            return [addresses.model_dump(exclude_none=True)]
        if isinstance(addresses, list):
            return [self._normalize_address(a) for a in addresses]
        return [self._normalize_address(addresses)]

    def _normalize_address(
        self,
        address: str | EmailAddress,
    ) -> dict[str, str]:
        """Normalize single address to dict."""
        if isinstance(address, str):
            return {"email": address}
        return address.model_dump(exclude_none=True)
