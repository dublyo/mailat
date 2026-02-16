"""Resource modules for mailat.co SDK"""

from .emails import EmailsResource
from .contacts import ContactsResource
from .campaigns import CampaignsResource
from .domains import DomainsResource
from .webhooks import WebhooksResource
from .templates import TemplatesResource

__all__ = [
    "EmailsResource",
    "ContactsResource",
    "CampaignsResource",
    "DomainsResource",
    "WebhooksResource",
    "TemplatesResource",
]
