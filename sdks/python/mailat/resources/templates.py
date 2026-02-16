"""Templates resource for managing email templates"""

from typing import TYPE_CHECKING, Any, Optional
import re
from ..types import Template

if TYPE_CHECKING:
    from ..client import Mailat


class TemplatesResource:
    """Resource for managing email templates."""

    def __init__(self, client: "Mailat"):
        self._client = client

    def create(
        self,
        name: str,
        subject: str,
        html_body: str,
        text_body: Optional[str] = None,
        description: Optional[str] = None,
    ) -> Template:
        """Create a new template."""
        payload = {
            "name": name,
            "subject": subject,
            "htmlBody": html_body,
        }
        if text_body:
            payload["textBody"] = text_body
        if description:
            payload["description"] = description

        data = self._client.post("/templates", json=payload)
        return Template(**data)

    def get(self, template_id: str) -> Template:
        """Get template by ID."""
        data = self._client.get(f"/templates/{template_id}")
        return Template(**data)

    def update(
        self,
        template_id: str,
        name: Optional[str] = None,
        subject: Optional[str] = None,
        html_body: Optional[str] = None,
        text_body: Optional[str] = None,
        description: Optional[str] = None,
        is_active: Optional[bool] = None,
    ) -> Template:
        """Update a template."""
        payload = {}
        if name:
            payload["name"] = name
        if subject:
            payload["subject"] = subject
        if html_body:
            payload["htmlBody"] = html_body
        if text_body:
            payload["textBody"] = text_body
        if description:
            payload["description"] = description
        if is_active is not None:
            payload["isActive"] = is_active

        data = self._client.put(f"/templates/{template_id}", json=payload)
        return Template(**data)

    def delete(self, template_id: str) -> None:
        """Delete a template."""
        self._client.delete(f"/templates/{template_id}")

    def list(
        self,
        page: int = 1,
        limit: int = 50,
        search: Optional[str] = None,
    ) -> list[Template]:
        """List all templates."""
        params = {"page": page, "limit": limit}
        if search:
            params["search"] = search

        data = self._client.get("/templates", params=params)
        return [Template(**t) for t in data]

    def preview(
        self,
        template_id: str,
        variables: Optional[dict[str, Any]] = None,
    ) -> dict[str, str]:
        """Preview template with sample data."""
        payload = {"variables": variables} if variables else {}
        return self._client.post(f"/templates/{template_id}/preview", json=payload)

    def get_variables(self, template_id: str) -> list[str]:
        """Get template variables."""
        template = self.get(template_id)
        return template.variables or []

    def validate(
        self,
        html_body: str,
        subject: Optional[str] = None,
        text_body: Optional[str] = None,
    ) -> dict[str, Any]:
        """Validate template syntax."""
        payload = {"htmlBody": html_body}
        if subject:
            payload["subject"] = subject
        if text_body:
            payload["textBody"] = text_body

        return self._client.post("/templates/validate", json=payload)

    def enable(self, template_id: str) -> Template:
        """Enable a template."""
        return self.update(template_id, is_active=True)

    def disable(self, template_id: str) -> Template:
        """Disable a template."""
        return self.update(template_id, is_active=False)

    @staticmethod
    def extract_variables(content: str) -> list[str]:
        """Extract variables from template content."""
        pattern = r"\{\{([^}]+)\}\}"
        matches = re.findall(pattern, content)
        variables = set()
        for match in matches:
            # Clean up variable name
            var = match.strip().lstrip("#/^").split()[0]
            if var and not var.startswith("!"):
                variables.add(var)
        return list(variables)
