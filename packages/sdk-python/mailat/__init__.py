"""
Mailat Python SDK

Official Python SDK for the self-hosted Mailat API.

Example:
    >>> from mailat import Mailat
    >>> client = Mailat(api_key="ue_your_api_key", base_url="https://mail.example.com")
    >>> result = client.emails.send(
    ...     from_address="sender@yourdomain.com",
    ...     to=["recipient@example.com"],
    ...     subject="Hello!",
    ...     html="<p>Welcome!</p>", idempotency_key="welcome-123"
    ... )
"""

from mailat.client import Mailat
from mailat.models import (
    SendEmailRequest,
    SendEmailResponse,
    BatchSendResponse,
    EmailStatusResponse,
    Template,
    Webhook,
    WebhookCall,
    DeliveryEvent,
    MailatError,
    InboxCounts,
    DMARC_REPORTS_FOLDER,
    DMARCReportsSettings,
    UpdateDMARCReportsSettings,
)

__version__ = "0.2.0"
__all__ = [
    "Mailat",
    "SendEmailRequest",
    "SendEmailResponse",
    "BatchSendResponse",
    "EmailStatusResponse",
    "Template",
    "Webhook",
    "WebhookCall",
    "DeliveryEvent",
    "MailatError",
    "InboxCounts",
    "DMARC_REPORTS_FOLDER",
    "DMARCReportsSettings",
    "UpdateDMARCReportsSettings",
]
