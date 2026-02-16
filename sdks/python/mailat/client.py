"""
Main Mailat client class
"""

import time
from typing import Any, Optional, TypeVar
import httpx

from .exceptions import (
    MailatError,
    AuthenticationError,
    RateLimitError,
    ValidationError,
    NotFoundError,
    ServerError,
)
from .resources.emails import EmailsResource
from .resources.contacts import ContactsResource
from .resources.campaigns import CampaignsResource
from .resources.domains import DomainsResource
from .resources.webhooks import WebhooksResource
from .resources.templates import TemplatesResource

T = TypeVar("T")

DEFAULT_BASE_URL = "https://api.mailat.co/api/v1"
DEFAULT_TIMEOUT = 30.0
DEFAULT_MAX_RETRIES = 3


class Mailat:
    """
    Main client for interacting with the mailat.co API.

    Example usage:
        ```python
        from mailat import Mailat

        client = Mailat(api_key="your-api-key")

        # Send an email
        email = client.emails.send(
            to="recipient@example.com",
            subject="Hello!",
            html="<h1>Hello World</h1>"
        )

        # Create a contact
        contact = client.contacts.create(
            email="user@example.com",
            first_name="John"
        )
        ```
    """

    def __init__(
        self,
        api_key: str,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = DEFAULT_TIMEOUT,
        max_retries: int = DEFAULT_MAX_RETRIES,
    ):
        """
        Initialize the Mailat client.

        Args:
            api_key: Your API key from mailat.co dashboard
            base_url: API base URL (default: https://api.mailat.co/api/v1)
            timeout: Request timeout in seconds (default: 30)
            max_retries: Maximum retry attempts for failed requests (default: 3)
        """
        if not api_key:
            raise ValueError("API key is required")

        self.api_key = api_key
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.max_retries = max_retries

        self._client = httpx.Client(
            base_url=self.base_url,
            timeout=timeout,
            headers={
                "Authorization": f"Bearer {api_key}",
                "Content-Type": "application/json",
                "User-Agent": "mailat-python/1.0.0",
            },
        )

        # Initialize resource classes
        self.emails = EmailsResource(self)
        self.contacts = ContactsResource(self)
        self.campaigns = CampaignsResource(self)
        self.domains = DomainsResource(self)
        self.webhooks = WebhooksResource(self)
        self.templates = TemplatesResource(self)

    def __enter__(self) -> "Mailat":
        return self

    def __exit__(self, *args: Any) -> None:
        self.close()

    def close(self) -> None:
        """Close the HTTP client connection."""
        self._client.close()

    def request(
        self,
        method: str,
        endpoint: str,
        params: Optional[dict[str, Any]] = None,
        json: Optional[dict[str, Any]] = None,
    ) -> Any:
        """
        Make an HTTP request to the API.

        Args:
            method: HTTP method (GET, POST, PUT, DELETE)
            endpoint: API endpoint path
            params: Query parameters
            json: Request body as JSON

        Returns:
            Parsed response data

        Raises:
            MailatError: On API errors
        """
        url = endpoint if endpoint.startswith("http") else f"{self.base_url}{endpoint}"

        last_error: Optional[Exception] = None

        for attempt in range(self.max_retries + 1):
            try:
                response = self._client.request(
                    method=method,
                    url=url,
                    params=params,
                    json=json,
                )

                # Parse response
                try:
                    data = response.json()
                except Exception:
                    data = {}

                # Handle errors
                if not response.is_success:
                    self._handle_error(response.status_code, data, response)

                return data.get("data", data)

            except (httpx.TimeoutException, httpx.NetworkError) as e:
                last_error = e
                if attempt < self.max_retries:
                    time.sleep(2 ** attempt)
                    continue
                raise MailatError(
                    f"Network error: {str(e)}",
                    status_code=0,
                )

            except RateLimitError as e:
                last_error = e
                if attempt < self.max_retries:
                    retry_after = e.retry_after or (2 ** attempt)
                    time.sleep(retry_after)
                    continue
                raise

        if last_error:
            raise last_error
        raise MailatError("Unknown error occurred")

    def _handle_error(
        self,
        status_code: int,
        data: dict[str, Any],
        response: httpx.Response,
    ) -> None:
        """Handle API error responses."""
        message = data.get("message", "Unknown error")

        if status_code == 401:
            raise AuthenticationError(message)

        if status_code == 404:
            raise NotFoundError(message)

        if status_code == 429:
            retry_after = response.headers.get("Retry-After")
            raise RateLimitError(
                message,
                retry_after=int(retry_after) if retry_after else None,
            )

        if status_code == 400:
            raise ValidationError(message, errors=data.get("errors"))

        if status_code >= 500:
            raise ServerError(message)

        raise MailatError(message, status_code=status_code, response=data)

    def get(
        self,
        endpoint: str,
        params: Optional[dict[str, Any]] = None,
    ) -> Any:
        """Make a GET request."""
        return self.request("GET", endpoint, params=params)

    def post(
        self,
        endpoint: str,
        json: Optional[dict[str, Any]] = None,
    ) -> Any:
        """Make a POST request."""
        return self.request("POST", endpoint, json=json)

    def put(
        self,
        endpoint: str,
        json: Optional[dict[str, Any]] = None,
    ) -> Any:
        """Make a PUT request."""
        return self.request("PUT", endpoint, json=json)

    def delete(self, endpoint: str) -> Any:
        """Make a DELETE request."""
        return self.request("DELETE", endpoint)
