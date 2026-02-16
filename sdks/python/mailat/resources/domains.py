"""Domains resource for managing email domains"""

from typing import TYPE_CHECKING
from ..types import Domain

if TYPE_CHECKING:
    from ..client import Mailat


class DomainsResource:
    """Resource for managing email domains."""

    def __init__(self, client: "Mailat"):
        self._client = client

    def create(self, domain: str) -> Domain:
        """Add a new domain."""
        data = self._client.post("/domains", json={"domain": domain})
        return Domain(**data)

    def get(self, domain_id: str) -> Domain:
        """Get domain by ID."""
        data = self._client.get(f"/domains/{domain_id}")
        return Domain(**data)

    def delete(self, domain_id: str) -> None:
        """Delete a domain."""
        self._client.delete(f"/domains/{domain_id}")

    def list(self) -> list[Domain]:
        """List all domains."""
        data = self._client.get("/domains")
        return [Domain(**d) for d in data]

    def verify(self, domain_id: str) -> dict:
        """Verify domain DNS records."""
        return self._client.post(f"/domains/{domain_id}/verify")

    def is_verified(self, domain_id: str) -> bool:
        """Check if a domain is fully verified."""
        domain = self.get(domain_id)
        return domain.verified and domain.mx_verified and domain.spf_verified and domain.dkim_verified

    def get_verification_status(self, domain_id: str) -> dict[str, bool]:
        """Get verification status breakdown."""
        domain = self.get(domain_id)
        return {
            "domain": domain.name,
            "verified": domain.verified,
            "mx": domain.mx_verified,
            "spf": domain.spf_verified,
            "dkim": domain.dkim_verified,
            "dmarc": domain.dmarc_verified,
        }
