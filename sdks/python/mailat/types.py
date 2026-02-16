"""
Type definitions for mailat.co SDK
"""

from datetime import datetime
from enum import Enum
from typing import Any, Optional
from pydantic import BaseModel, Field


class EmailAddress(BaseModel):
    """Email address with optional display name"""
    email: str
    name: Optional[str] = None


class Attachment(BaseModel):
    """Email attachment"""
    filename: str
    content: str | bytes
    content_type: Optional[str] = None
    content_id: Optional[str] = None


class SendEmailOptions(BaseModel):
    """Options for sending an email"""
    to: str | list[str] | EmailAddress | list[EmailAddress]
    cc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None
    bcc: Optional[str | list[str] | EmailAddress | list[EmailAddress]] = None
    from_address: Optional[str | EmailAddress] = Field(None, alias="from")
    reply_to: Optional[str | EmailAddress] = None
    subject: str
    text: Optional[str] = None
    html: Optional[str] = None
    template_id: Optional[str] = None
    template_data: Optional[dict[str, Any]] = None
    attachments: Optional[list[Attachment]] = None
    tags: Optional[list[str]] = None
    metadata: Optional[dict[str, Any]] = None
    headers: Optional[dict[str, str]] = None
    scheduled_at: Optional[datetime] = None
    idempotency_key: Optional[str] = None

    class Config:
        populate_by_name = True


class EmailStatus(str, Enum):
    """Email delivery status"""
    QUEUED = "queued"
    SENDING = "sending"
    SENT = "sent"
    DELIVERED = "delivered"
    OPENED = "opened"
    CLICKED = "clicked"
    BOUNCED = "bounced"
    COMPLAINED = "complained"
    FAILED = "failed"
    CANCELLED = "cancelled"


class Email(BaseModel):
    """Email record"""
    id: str
    uuid: str
    message_id: str
    status: EmailStatus
    from_address: EmailAddress = Field(alias="from")
    to: list[EmailAddress]
    cc: Optional[list[EmailAddress]] = None
    bcc: Optional[list[EmailAddress]] = None
    subject: str
    tags: Optional[list[str]] = None
    metadata: Optional[dict[str, Any]] = None
    sent_at: Optional[datetime] = None
    delivered_at: Optional[datetime] = None
    opened_at: Optional[datetime] = None
    clicked_at: Optional[datetime] = None
    bounced_at: Optional[datetime] = None
    created_at: datetime

    class Config:
        populate_by_name = True


class EmailEvent(BaseModel):
    """Email delivery event"""
    id: str
    email_id: str
    event_type: str
    timestamp: datetime
    data: Optional[dict[str, Any]] = None


class ContactStatus(str, Enum):
    """Contact subscription status"""
    ACTIVE = "active"
    UNSUBSCRIBED = "unsubscribed"
    BOUNCED = "bounced"
    COMPLAINED = "complained"


class Contact(BaseModel):
    """Marketing contact"""
    id: str
    uuid: str
    email: str
    first_name: Optional[str] = None
    last_name: Optional[str] = None
    status: ContactStatus = ContactStatus.ACTIVE
    attributes: Optional[dict[str, Any]] = None
    tags: Optional[list[str]] = None
    engagement_score: Optional[float] = None
    created_at: datetime
    updated_at: datetime


class ContactList(BaseModel):
    """Contact list"""
    id: str
    uuid: str
    name: str
    description: Optional[str] = None
    type: str = "static"
    contact_count: int = 0
    created_at: datetime
    updated_at: datetime


class CampaignStatus(str, Enum):
    """Campaign status"""
    DRAFT = "draft"
    SCHEDULED = "scheduled"
    SENDING = "sending"
    SENT = "sent"
    PAUSED = "paused"
    CANCELLED = "cancelled"


class CampaignStats(BaseModel):
    """Campaign statistics"""
    total: int = 0
    sent: int = 0
    delivered: int = 0
    opened: int = 0
    clicked: int = 0
    bounced: int = 0
    unsubscribed: int = 0
    complained: int = 0

    @property
    def open_rate(self) -> float:
        return (self.opened / self.delivered * 100) if self.delivered > 0 else 0

    @property
    def click_rate(self) -> float:
        return (self.clicked / self.delivered * 100) if self.delivered > 0 else 0


class Campaign(BaseModel):
    """Marketing campaign"""
    id: str
    uuid: str
    name: str
    subject: str
    status: CampaignStatus = CampaignStatus.DRAFT
    list_id: str
    template_id: Optional[str] = None
    html_content: Optional[str] = None
    text_content: Optional[str] = None
    from_name: str
    from_email: str
    reply_to: Optional[str] = None
    scheduled_at: Optional[datetime] = None
    sent_at: Optional[datetime] = None
    stats: CampaignStats = Field(default_factory=CampaignStats)
    created_at: datetime
    updated_at: datetime


class DomainStatus(str, Enum):
    """Domain verification status"""
    PENDING = "pending"
    ACTIVE = "active"
    SUSPENDED = "suspended"


class DnsRecord(BaseModel):
    """DNS record for domain verification"""
    type: str
    name: str
    value: str
    verified: bool = False
    last_checked_at: Optional[datetime] = None


class Domain(BaseModel):
    """Email sending domain"""
    id: str
    uuid: str
    name: str
    verified: bool = False
    status: DomainStatus = DomainStatus.PENDING
    mx_verified: bool = False
    spf_verified: bool = False
    dkim_verified: bool = False
    dmarc_verified: bool = False
    dns_records: list[DnsRecord] = Field(default_factory=list)
    created_at: datetime
    updated_at: datetime


class Template(BaseModel):
    """Email template"""
    id: str
    uuid: str
    name: str
    description: Optional[str] = None
    subject: str
    html_body: str
    text_body: Optional[str] = None
    variables: Optional[list[str]] = None
    is_active: bool = True
    created_at: datetime
    updated_at: datetime


class WebhookEvent(str, Enum):
    """Webhook event types"""
    EMAIL_SENT = "email.sent"
    EMAIL_DELIVERED = "email.delivered"
    EMAIL_OPENED = "email.opened"
    EMAIL_CLICKED = "email.clicked"
    EMAIL_BOUNCED = "email.bounced"
    EMAIL_COMPLAINED = "email.complained"
    CONTACT_CREATED = "contact.created"
    CONTACT_UPDATED = "contact.updated"
    CONTACT_UNSUBSCRIBED = "contact.unsubscribed"
    CAMPAIGN_SENT = "campaign.sent"
    CAMPAIGN_COMPLETED = "campaign.completed"


class Webhook(BaseModel):
    """Webhook endpoint"""
    id: str
    uuid: str
    name: str
    url: str
    events: list[WebhookEvent]
    active: bool = True
    secret: Optional[str] = None
    success_count: int = 0
    failure_count: int = 0
    last_triggered_at: Optional[datetime] = None
    created_at: datetime


class WebhookCall(BaseModel):
    """Webhook delivery record"""
    id: str
    event_type: str
    payload: dict[str, Any]
    response_status: Optional[int] = None
    response_time_ms: Optional[int] = None
    status: str = "pending"
    attempts: int = 0
    error: Optional[str] = None
    created_at: datetime
