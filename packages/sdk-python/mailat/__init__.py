"""
mailat.co Python SDK

Official Python SDK for the mailat.co API.

Example:
    >>> from mailat import Mailat
    >>> client = Mailat(api_key="ue_your_api_key")
    >>> result = client.emails.send(
    ...     from_address="sender@yourdomain.com",
    ...     to=["recipient@example.com"],
    ...     subject="Hello!",
    ...     html="<p>Welcome!</p>"
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
)

__version__ = "0.1.0"
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
]
