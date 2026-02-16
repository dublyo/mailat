"""Webhooks resource for managing webhook endpoints"""

from typing import TYPE_CHECKING, Optional
import hmac
import hashlib
from ..types import Webhook, WebhookEvent, WebhookCall

if TYPE_CHECKING:
    from ..client import Mailat


class WebhooksResource:
    """Resource for managing webhook endpoints."""

    def __init__(self, client: "Mailat"):
        self._client = client

    def create(
        self,
        name: str,
        url: str,
        events: list[WebhookEvent | str],
    ) -> Webhook:
        """Create a new webhook."""
        event_list = [e.value if isinstance(e, WebhookEvent) else e for e in events]
        data = self._client.post("/webhooks", json={
            "name": name,
            "url": url,
            "events": event_list,
        })
        return Webhook(**data)

    def get(self, webhook_id: str) -> Webhook:
        """Get webhook by ID."""
        data = self._client.get(f"/webhooks/{webhook_id}")
        return Webhook(**data)

    def update(
        self,
        webhook_id: str,
        name: Optional[str] = None,
        url: Optional[str] = None,
        events: Optional[list[WebhookEvent | str]] = None,
        active: Optional[bool] = None,
    ) -> Webhook:
        """Update a webhook."""
        payload = {}
        if name:
            payload["name"] = name
        if url:
            payload["url"] = url
        if events:
            payload["events"] = [e.value if isinstance(e, WebhookEvent) else e for e in events]
        if active is not None:
            payload["active"] = active

        data = self._client.put(f"/webhooks/{webhook_id}", json=payload)
        return Webhook(**data)

    def delete(self, webhook_id: str) -> None:
        """Delete a webhook."""
        self._client.delete(f"/webhooks/{webhook_id}")

    def list(self) -> list[Webhook]:
        """List all webhooks."""
        data = self._client.get("/webhooks")
        return [Webhook(**w) for w in data]

    def enable(self, webhook_id: str) -> Webhook:
        """Enable a webhook."""
        return self.update(webhook_id, active=True)

    def disable(self, webhook_id: str) -> Webhook:
        """Disable a webhook."""
        return self.update(webhook_id, active=False)

    def rotate_secret(self, webhook_id: str) -> dict[str, str]:
        """Rotate webhook secret."""
        return self._client.post(f"/webhooks/{webhook_id}/rotate-secret")

    def test(self, webhook_id: str) -> dict:
        """Test a webhook."""
        return self._client.post(f"/webhooks/{webhook_id}/test")

    def get_calls(
        self,
        webhook_id: str,
        page: int = 1,
        limit: int = 50,
        status: Optional[str] = None,
    ) -> list[WebhookCall]:
        """Get recent webhook calls."""
        params = {"page": page, "limit": limit}
        if status:
            params["status"] = status

        data = self._client.get(f"/webhooks/{webhook_id}/calls", params=params)
        return [WebhookCall(**c) for c in data]

    @staticmethod
    def get_event_types() -> list[str]:
        """Get available webhook event types."""
        return [e.value for e in WebhookEvent]

    @staticmethod
    def verify_signature(payload: str | bytes, signature: str, secret: str) -> bool:
        """Verify webhook signature."""
        if isinstance(payload, str):
            payload = payload.encode()

        expected = hmac.new(
            secret.encode(),
            payload,
            hashlib.sha256
        ).hexdigest()

        provided = signature.replace("sha256=", "")
        return hmac.compare_digest(expected, provided)
