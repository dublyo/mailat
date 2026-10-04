"""Data models for mailat.co SDK."""

from datetime import datetime
from enum import Enum
from typing import Any, Dict, List, Literal, Optional

from pydantic import BaseModel, AliasChoices, ConfigDict, Field, field_validator

def _camel(name: str) -> str:
    head, *tail = name.split("_")
    return head + "".join(part.capitalize() for part in tail)

class WireModel(BaseModel):
    model_config = ConfigDict(alias_generator=_camel, populate_by_name=True)

    @field_validator("*", mode="before")
    @classmethod
    def empty_lists(cls, value, info):
        annotation = cls.model_fields[info.field_name].annotation
        if value is None and getattr(annotation, "__origin__", None) is list:
            return []
        return value


DMARC_REPORTS_FOLDER = "dmarc-reports"
MailboxFolder = Literal["inbox", "dmarc-reports", "sent", "drafts", "outbox", "archive", "spam", "trash"]
InboxView = Literal["inbox", "dmarc-reports", "sent", "drafts", "outbox", "archive", "spam", "trash", "all", "starred"]
MailboxMoveDestination = Literal["inbox", "dmarc-reports", "archive", "spam", "trash"]


class InboxCounts(WireModel):
    inbox: int
    inbox_unread: int
    dmarc_reports: int
    dmarc_reports_unread: int
    unread: int  # Global total; use inbox_unread for the main Inbox badge.
    starred: int
    sent: int
    drafts: int
    spam: int
    trash: int
    labels: Dict[str, int] = Field(default_factory=dict)


class DMARCReportsSettings(WireModel):
    """Human-session-only preference; API keys cannot update account settings."""
    auto_organize_dmarc_reports: bool


class UpdateDMARCReportsSettings(WireModel):
    """Serialize with exclude_none=True: omission preserves, False opts out."""
    auto_organize_dmarc_reports: Optional[bool] = None



class EmailStatus(str, Enum):
    """Email delivery status."""
    QUEUED = "queued"
    SENDING = "sending"
    SENT = "sent"
    DELIVERED = "delivered"
    BOUNCED = "bounced"
    FAILED = "failed"
    CANCELLED = "cancelled"
    UNKNOWN = "unknown"
    ACCEPTED = "accepted"
    COMPLAINED = "complained"


class WebhookEvent(str, Enum):
    """Webhook event types."""
    EMAIL_RECEIVED = "email.received"
    EMAIL_UNKNOWN = "email.unknown"
    WEBHOOK_TEST = "webhook.test"
    EMAIL_SENT = "email.sent"
    EMAIL_DELIVERED = "email.delivered"
    EMAIL_BOUNCED = "email.bounced"
    EMAIL_COMPLAINED = "email.complained"
    EMAIL_FAILED = "email.failed"


# Request models

class Attachment(WireModel):
    """Email attachment."""
    name: str = Field(validation_alias=AliasChoices("name", "filename"))
    content: str  # Base64 encoded
    type: str = Field(validation_alias=AliasChoices("type", "content_type", "contentType"))
    disposition: Optional[str] = None
    cid: Optional[str] = None


class SendEmailRequest(WireModel):
    """Request to send an email."""
    from_address: str = Field(alias="from")
    to: List[str]
    cc: Optional[List[str]] = None
    bcc: Optional[List[str]] = None
    reply_to: Optional[str] = None
    subject: str
    html: Optional[str] = None
    text: Optional[str] = None
    template_id: Optional[str] = None
    variables: Optional[Dict[str, str]] = None
    attachments: Optional[List[Attachment]] = None
    tags: Optional[List[str]] = None
    metadata: Optional[Dict[str, str]] = None
    scheduled_for: Optional[str] = None
    idempotency_key: Optional[str] = None



class CreateTemplateRequest(WireModel):
    """Request to create a template."""
    name: str
    description: Optional[str] = None
    subject: str
    html: str
    text: Optional[str] = None


class UpdateTemplateRequest(WireModel):
    """Request to update a template."""
    name: Optional[str] = None
    description: Optional[str] = None
    subject: Optional[str] = None
    html: Optional[str] = None
    text: Optional[str] = None
    is_active: Optional[bool] = None


class CreateWebhookRequest(WireModel):
    """Request to create a webhook."""
    name: str
    url: str
    events: List[WebhookEvent]


class UpdateWebhookRequest(WireModel):
    """Request to update a webhook."""
    name: Optional[str] = None
    url: Optional[str] = None
    events: Optional[List[WebhookEvent]] = None
    active: Optional[bool] = None


# Response models

class DeliveryEvent(WireModel):
    """Email delivery event."""
    id: int
    email_id: int
    event_type: str
    timestamp: datetime
    details: Optional[str] = None
    ip_address: Optional[str] = None
    user_agent: Optional[str] = None


class SendEmailResponse(WireModel):
    """Response from sending an email."""
    id: str
    message_id: str
    status: EmailStatus
    accepted_at: datetime


class BatchEmailResult(WireModel):
    """Result for a single email in a batch."""
    index: int
    id: Optional[str] = None
    message_id: Optional[str] = None
    status: str
    error: Optional[str] = None


class BatchSendResponse(WireModel):
    """Response from batch sending emails."""
    results: List[BatchEmailResult]


class EmailStatusResponse(WireModel):
    """Response with email status and events."""
    id: str
    message_id: str
    from_address: str = Field(alias="from")
    to: List[str]
    subject: str
    status: EmailStatus
    events: List[DeliveryEvent]
    created_at: datetime
    sent_at: Optional[datetime] = None
    delivered_at: Optional[datetime] = None



class Template(WireModel):
    """Email template."""
    id: int
    uuid: str
    name: str
    description: Optional[str] = None
    subject: str
    html_body: str
    text_body: Optional[str] = None
    variables: Optional[List[str]] = None
    is_active: bool
    created_at: datetime
    updated_at: datetime


class PreviewTemplateResponse(WireModel):
    """Response from previewing a template."""
    subject: str
    html: str
    text: str


class Webhook(WireModel):
    """Webhook endpoint."""
    id: int
    uuid: str
    name: str
    url: str
    events: List[WebhookEvent]
    active: bool
    secret: Optional[str] = None  # Only on creation
    success_count: int = 0
    failure_count: int = 0
    last_triggered_at: Optional[datetime] = None
    last_success_at: Optional[datetime] = None
    last_failure_at: Optional[datetime] = None
    created_at: datetime
    updated_at: datetime


class WebhookCall(WireModel):
    """Webhook delivery attempt."""
    id: int
    event_type: str
    payload: Dict[str, Any]
    response_status: Optional[int] = None
    response_body: Optional[str] = None
    response_time_ms: Optional[int] = None
    status: str
    attempts: int
    error: Optional[str] = None
    created_at: datetime
    completed_at: Optional[datetime] = None


class WebhookPayload(WireModel):
    """Webhook event payload."""
    version: str
    id: str
    type: str
    created_at: datetime
    data: Dict[str, Any]


# Error

class MailatError(Exception):
    """Exception raised for API errors."""

    def __init__(self, message: str, status: int, code: Any = None, retry_after: Optional[str] = None):
        self.message = message
        self.status = status
        self.code = code
        self.retry_after = retry_after
        super().__init__(message)

    def __str__(self) -> str:
        return f"MailatError({self.status}): {self.message}"
