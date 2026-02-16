"""Campaigns resource for managing marketing campaigns"""

from typing import TYPE_CHECKING, Any, Optional
from datetime import datetime
from ..types import Campaign, CampaignStats

if TYPE_CHECKING:
    from ..client import Mailat


class CampaignsResource:
    """Resource for managing marketing campaigns."""

    def __init__(self, client: "Mailat"):
        self._client = client

    def create(
        self,
        name: str,
        subject: str,
        list_ids: list[str],
        from_name: str,
        from_email: str,
        html_content: Optional[str] = None,
        text_content: Optional[str] = None,
        template_id: Optional[str] = None,
        reply_to: Optional[str] = None,
    ) -> Campaign:
        """Create a new campaign."""
        payload = {
            "name": name,
            "subject": subject,
            "listIds": list_ids,
            "fromName": from_name,
            "fromEmail": from_email,
        }
        if html_content:
            payload["htmlContent"] = html_content
        if text_content:
            payload["textContent"] = text_content
        if template_id:
            payload["templateId"] = template_id
        if reply_to:
            payload["replyTo"] = reply_to

        data = self._client.post("/campaigns", json=payload)
        return Campaign(**data)

    def get(self, campaign_id: str) -> Campaign:
        """Get campaign by ID."""
        data = self._client.get(f"/campaigns/{campaign_id}")
        return Campaign(**data)

    def update(
        self,
        campaign_id: str,
        name: Optional[str] = None,
        subject: Optional[str] = None,
        html_content: Optional[str] = None,
        text_content: Optional[str] = None,
    ) -> Campaign:
        """Update a campaign."""
        payload = {}
        if name:
            payload["name"] = name
        if subject:
            payload["subject"] = subject
        if html_content:
            payload["htmlContent"] = html_content
        if text_content:
            payload["textContent"] = text_content

        data = self._client.put(f"/campaigns/{campaign_id}", json=payload)
        return Campaign(**data)

    def delete(self, campaign_id: str) -> None:
        """Delete a campaign."""
        self._client.delete(f"/campaigns/{campaign_id}")

    def list(
        self,
        page: int = 1,
        limit: int = 50,
        status: Optional[str] = None,
    ) -> dict[str, Any]:
        """List campaigns."""
        params = {"page": page, "limit": limit}
        if status:
            params["status"] = status
        return self._client.get("/campaigns", params=params)

    def send(self, campaign_id: str) -> Campaign:
        """Send campaign immediately."""
        data = self._client.post(f"/campaigns/{campaign_id}/send")
        return Campaign(**data)

    def schedule(self, campaign_id: str, scheduled_at: datetime) -> Campaign:
        """Schedule campaign for future sending."""
        data = self._client.post(
            f"/campaigns/{campaign_id}/schedule",
            json={"scheduledAt": scheduled_at.isoformat()},
        )
        return Campaign(**data)

    def pause(self, campaign_id: str) -> Campaign:
        """Pause a sending campaign."""
        data = self._client.post(f"/campaigns/{campaign_id}/pause")
        return Campaign(**data)

    def resume(self, campaign_id: str) -> Campaign:
        """Resume a paused campaign."""
        data = self._client.post(f"/campaigns/{campaign_id}/resume")
        return Campaign(**data)

    def cancel(self, campaign_id: str) -> Campaign:
        """Cancel a scheduled or sending campaign."""
        data = self._client.post(f"/campaigns/{campaign_id}/cancel")
        return Campaign(**data)

    def get_stats(self, campaign_id: str) -> CampaignStats:
        """Get campaign statistics."""
        data = self._client.get(f"/campaigns/{campaign_id}/stats")
        return CampaignStats(**data)

    def preview(self, campaign_id: str, contact_id: Optional[str] = None) -> dict[str, str]:
        """Preview campaign HTML."""
        payload = {"contactId": contact_id} if contact_id else {}
        return self._client.post(f"/campaigns/{campaign_id}/preview", json=payload)

    def send_test(self, campaign_id: str, emails: list[str]) -> dict[str, int]:
        """Send test email for campaign."""
        return self._client.post(f"/campaigns/{campaign_id}/test", json={"emails": emails})
