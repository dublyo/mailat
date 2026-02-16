"""
mailat.co Python SDK
Official SDK for interacting with the mailat.co API
"""

from .client import Mailat
from .types import (
    Email,
    EmailAddress,
    SendEmailOptions,
    Contact,
    ContactList,
    Campaign,
    CampaignStats,
    Domain,
    DnsRecord,
    Template,
    Webhook,
    WebhookEvent,
)
from .exceptions import (
    MailatError,
    AuthenticationError,
    RateLimitError,
    ValidationError,
    NotFoundError,
)

__version__ = "1.0.0"
__all__ = [
    "Mailat",
    # Types
    "Email",
    "EmailAddress",
    "SendEmailOptions",
    "Contact",
    "ContactList",
    "Campaign",
    "CampaignStats",
    "Domain",
    "DnsRecord",
    "Template",
    "Webhook",
    "WebhookEvent",
    # Exceptions
    "MailatError",
    "AuthenticationError",
    "RateLimitError",
    "ValidationError",
    "NotFoundError",
]
