"""
Exception classes for mailat.co SDK
"""

from typing import Any, Optional


class MailatError(Exception):
    """Base exception for mailat.co SDK errors"""

    def __init__(
        self,
        message: str,
        status_code: int = 500,
        response: Optional[Any] = None
    ):
        super().__init__(message)
        self.message = message
        self.status_code = status_code
        self.response = response

    def __str__(self) -> str:
        return f"{self.message} (status={self.status_code})"


class AuthenticationError(MailatError):
    """Raised when API key is invalid or missing"""

    def __init__(self, message: str = "Invalid or missing API key"):
        super().__init__(message, status_code=401)


class RateLimitError(MailatError):
    """Raised when rate limit is exceeded"""

    def __init__(
        self,
        message: str = "Rate limit exceeded",
        retry_after: Optional[int] = None
    ):
        super().__init__(message, status_code=429)
        self.retry_after = retry_after


class ValidationError(MailatError):
    """Raised when request validation fails"""

    def __init__(
        self,
        message: str,
        errors: Optional[dict[str, list[str]]] = None
    ):
        super().__init__(message, status_code=400)
        self.errors = errors or {}


class NotFoundError(MailatError):
    """Raised when requested resource is not found"""

    def __init__(self, message: str = "Resource not found"):
        super().__init__(message, status_code=404)


class ServerError(MailatError):
    """Raised when server returns an error"""

    def __init__(self, message: str = "Internal server error"):
        super().__init__(message, status_code=500)
