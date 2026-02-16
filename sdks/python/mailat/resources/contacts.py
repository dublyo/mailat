"""Contacts resource for managing marketing contacts"""

from typing import TYPE_CHECKING, Any, Optional
from ..types import Contact, ContactList

if TYPE_CHECKING:
    from ..client import Mailat


class ContactsResource:
    """Resource for managing marketing contacts."""

    def __init__(self, client: "Mailat"):
        self._client = client

    def create(
        self,
        email: str,
        first_name: Optional[str] = None,
        last_name: Optional[str] = None,
        attributes: Optional[dict[str, Any]] = None,
        tags: Optional[list[str]] = None,
        list_ids: Optional[list[str]] = None,
    ) -> Contact:
        """Create a new contact."""
        payload = {"email": email}
        if first_name:
            payload["firstName"] = first_name
        if last_name:
            payload["lastName"] = last_name
        if attributes:
            payload["attributes"] = attributes
        if tags:
            payload["tags"] = tags
        if list_ids:
            payload["listIds"] = list_ids

        data = self._client.post("/contacts", json=payload)
        return Contact(**data)

    def get(self, id_or_email: str) -> Contact:
        """Get contact by ID or email."""
        data = self._client.get(f"/contacts/{id_or_email}")
        return Contact(**data)

    def update(
        self,
        contact_id: str,
        first_name: Optional[str] = None,
        last_name: Optional[str] = None,
        attributes: Optional[dict[str, Any]] = None,
        tags: Optional[list[str]] = None,
    ) -> Contact:
        """Update a contact."""
        payload = {}
        if first_name is not None:
            payload["firstName"] = first_name
        if last_name is not None:
            payload["lastName"] = last_name
        if attributes is not None:
            payload["attributes"] = attributes
        if tags is not None:
            payload["tags"] = tags

        data = self._client.put(f"/contacts/{contact_id}", json=payload)
        return Contact(**data)

    def delete(self, contact_id: str) -> None:
        """Delete a contact."""
        self._client.delete(f"/contacts/{contact_id}")

    def list(
        self,
        page: int = 1,
        limit: int = 50,
        status: Optional[str] = None,
        tag: Optional[str] = None,
        search: Optional[str] = None,
    ) -> dict[str, Any]:
        """List contacts with pagination."""
        params = {"page": page, "limit": limit}
        if status:
            params["status"] = status
        if tag:
            params["tag"] = tag
        if search:
            params["search"] = search

        return self._client.get("/contacts", params=params)

    def search(self, query: str, page: int = 1, limit: int = 50) -> list[Contact]:
        """Search contacts."""
        data = self._client.get("/contacts/search", params={"q": query, "page": page, "limit": limit})
        return [Contact(**c) for c in data]

    def import_contacts(
        self,
        contacts: list[dict[str, Any]],
        list_id: Optional[str] = None,
        tags: Optional[list[str]] = None,
    ) -> dict[str, Any]:
        """Import contacts in bulk."""
        payload = {"contacts": contacts}
        if list_id:
            payload["listId"] = list_id
        if tags:
            payload["tags"] = tags

        return self._client.post("/contacts/import", json=payload)

    def unsubscribe(self, email: str, list_id: Optional[str] = None) -> Contact:
        """Unsubscribe a contact."""
        payload = {"email": email}
        if list_id:
            payload["listId"] = list_id

        data = self._client.post("/contacts/unsubscribe", json=payload)
        return Contact(**data)

    def add_tags(self, contact_id: str, tags: list[str]) -> Contact:
        """Add tags to a contact."""
        contact = self.get(contact_id)
        existing_tags = contact.tags or []
        new_tags = list(set(existing_tags + tags))
        return self.update(contact_id, tags=new_tags)

    def remove_tags(self, contact_id: str, tags: list[str]) -> Contact:
        """Remove tags from a contact."""
        contact = self.get(contact_id)
        existing_tags = contact.tags or []
        new_tags = [t for t in existing_tags if t not in tags]
        return self.update(contact_id, tags=new_tags)

    def get_lists(self, contact_id: str) -> list[ContactList]:
        """Get lists a contact belongs to."""
        data = self._client.get(f"/contacts/{contact_id}/lists")
        return [ContactList(**l) for l in data]
