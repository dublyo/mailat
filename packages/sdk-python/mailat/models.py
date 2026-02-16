"""Data models for mailat.co SDK."""

from datetime import datetime
from enum import Enum
from typing import Any, Dict, List, Optional

from pydantic import BaseModel, Field


class EmailStatus(str, Enum):
    """Email delivery status."""
    QUEUED = "queued"
    SENDING = "sending"
    SENT = "sent"
    DELIVERED = "delivered"
    BOUNCED = "bounced"
    FAILED = "failed"
    CANCELLED = "cancelled"


class WebhookEvent(str, Enum):
    """Webhook event types."""
    EMAIL_SENT = "email.sent"
    EMAIL_DELIVERED = "email.delivered"
    EMAIL_BOUNCED = "email.bounced"
    EMAIL_COMPLAINED = "email.complained"
    EMAIL_OPENED = "email.opened"
    EMAIL_CLICKED = "email.clicked"
    EMAIL_FAILED = "email.failed"


# Request models

class Attachment(BaseModel):
    """Email attachment."""
    filename: str
    content: str  # Base64 encoded
    content_type: str
    cid: Optional[str] = None


class SendEmailRequest(BaseModel):
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

    class Config:
        populate_by_name = True


class CreateTemplateRequest(BaseModel):
    """Request to create a template."""
    name: str
    description: Optional[str] = None
    subject: str
    html: str
    text: Optional[str] = None


class UpdateTemplateRequest(BaseModel):
    """Request to update a template."""
    name: Optional[str] = None
    description: Optional[str] = None
    subject: Optional[str] = None
    html: Optional[str] = None
    text: Optional[str] = None
    is_active: Optional[bool] = None


class CreateWebhookRequest(BaseModel):
    """Request to create a webhook."""
    name: str
    url: str
    events: List[WebhookEvent]


class UpdateWebhookRequest(BaseModel):
    """Request to update a webhook."""
    name: Optional[str] = None
    url: Optional[str] = None
    events: Optional[List[WebhookEvent]] = None
    active: Optional[bool] = None


# Response models

class DeliveryEvent(BaseModel):
    """Email delivery event."""
    id: int
    email_id: int
    event_type: str
    timestamp: datetime
    details: Optional[str] = None
    ip_address: Optional[str] = None
    user_agent: Optional[str] = None


class SendEmailResponse(BaseModel):
    """Response from sending an email."""
    id: str
    message_id: str
    status: EmailStatus
    accepted_at: datetime


class BatchEmailResult(BaseModel):
    """Result for a single email in a batch."""
    index: int
    id: Optional[str] = None
    message_id: Optional[str] = None
    status: str
    error: Optional[str] = None


class BatchSendResponse(BaseModel):
    """Response from batch sending emails."""
    results: List[BatchEmailResult]


class EmailStatusResponse(BaseModel):
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

    class Config:
        populate_by_name = True


class Template(BaseModel):
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


class PreviewTemplateResponse(BaseModel):
    """Response from previewing a template."""
    subject: str
    html: str
    text: str


class Webhook(BaseModel):
    """Webhook endpoint."""
    id: int
    uuid: str
    name: str
    url: str
    events: List[WebhookEvent]
    active: bool
    secret: Optional[str] = None  # Only on creation
    success_count: int
    failure_count: int
    last_triggered_at: Optional[datetime] = None
    last_success_at: Optional[datetime] = None
    last_failure_at: Optional[datetime] = None
    created_at: datetime
    updated_at: datetime


class WebhookCall(BaseModel):
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


class WebhookPayload(BaseModel):
    """Webhook event payload."""
    type: str
    created_at: int
    data: Dict[str, Any]


# Error

class MailatError(Exception):
    """Exception raised for API errors."""

    def __init__(self, message: str, status: int, code: Optional[str] = None):
        self.message = message
        self.status = status
        self.code = code
        super().__init__(message)

    def __str__(self) -> str:
        return f"MailatError({self.status}): {self.message}"
