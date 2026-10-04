"""Core automation resources. Bodies use the API's documented camelCase fields."""
from urllib.parse import quote, urlencode
from mailat.models import InboxCounts


def send_headers(key):
    if not isinstance(key, str) or not 8 <= len(key) <= 128:
        raise ValueError("A stable 8–128 character idempotency key is required")
    return {"Idempotency-Key": key}


def _id(value):
    return quote(str(value), safe="")


def _query(options):
    values = {k: str(v).lower() if isinstance(v, bool) else v for k, v in options.items() if v is not None}
    return "?" + urlencode(values) if values else ""


class _List:
    def __init__(self, client, path):
        self._client, self._path = client, path

    def list(self, **options):
        return self._client._request("GET", self._path + _query(options))["data"] or []


class _Read(_List):
    def get(self, uuid):
        return self._client._request("GET", self._path + "/" + _id(uuid))["data"]


class Labels(_List):
    def create(self, body):
        return self._client._request("POST", self._path, body)["data"]

    def update(self, uuid, body):
        return self._client._request("PUT", self._path + "/" + _id(uuid), body)["data"]

    def delete(self, uuid):
        self._client._request("DELETE", self._path + "/" + _id(uuid))


class _CRUD(Labels, _Read):
    pass


class Inbox(_Read):
    def __init__(self, client):
        super().__init__(client, "/inbox/received")
        self.labels = Labels(client, "/inbox/labels")
        self.filters = _CRUD(client, "/inbox/filters")

    def counts(self):
        return self._client._request("GET", self._path + "/counts")["data"]

    def folder_counts(self, identity_id=None) -> InboxCounts:
        """Typed counts; omitted identity selects the unified owned mailbox."""
        response = self._client._request("GET", self._path + "/counts" + _query({"identityId": identity_id}))
        return InboxCounts(**response["data"])

    def setup_receiving(self, domain_id):
        return self._client._request("POST", "/inbox/setup", {"domainId": domain_id})["data"]

    def changes(self, cursor=None, limit=100):
        return self._client._request("GET", "/inbox/changes" + _query({"cursor": cursor, "limit": limit}))["data"]

    def attachment(self, message_uuid, attachment_uuid):
        return self._client._request("GET", self._path + "/" + _id(message_uuid) + "/attachments/" + _id(attachment_uuid), binary=True)

    def mark(self, email_uuids, is_read):
        self._client._request("POST", self._path + "/mark", {"emailUuids": email_uuids, "isRead": is_read})

    def star(self, email_uuids, is_starred):
        self._client._request("POST", self._path + "/star", {"emailUuids": email_uuids, "isStarred": is_starred})

    def move(self, email_uuids, folder):
        self._client._request("POST", self._path + "/move", {"emailUuids": email_uuids, "folder": folder})

    def trash(self, email_uuids, permanent=False):
        self._client._request("POST", self._path + "/trash", {"emailUuids": email_uuids, "permanent": permanent})

    def assign_labels(self, email_uuids, add_labels, remove_labels=None):
        self._client._request("POST", self._path + "/labels", {"emailUuids": email_uuids, "addLabels": add_labels, "removeLabels": remove_labels or []})

    def test_filter(self, uuid, body):
        return self._client._request("POST", "/inbox/filters/" + _id(uuid) + "/test", body)["data"]


class Compose:
    def __init__(self, client):
        self._client = client

    def reply_context(self, uuid, reply_all=False):
        return self._client._request("GET", "/compose/reply/" + _id(uuid) + _query({"replyAll": reply_all}))["data"]

    def forward_context(self, uuid):
        return self._client._request("GET", "/compose/forward/" + _id(uuid))["data"]

    def send(self, body, idempotency_key):
        return self._client._request("POST", "/compose/send", body, send_headers(idempotency_key))["data"]

    def save_draft(self, body):
        return self._client._request("POST", "/compose/drafts", body)["data"]

    def update_draft(self, uuid, body):
        return self._client._request("PUT", "/compose/drafts/" + _id(uuid), body)["data"]

    def delete_draft(self, uuid):
        self._client._request("DELETE", "/compose/drafts/" + _id(uuid))


class Domains(_Read):
    def __init__(self, client):
        super().__init__(client, "/domains")

    def create(self, body):
        return self._client._request("POST", self._path, body)["data"]

    def delete(self, uuid):
        self._client._request("DELETE", self._path + "/" + _id(uuid))

    def verify(self, uuid):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/verify")["data"]

    def initiate_ses(self, uuid):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/ses-verify")["data"]

    def ses_status(self, uuid):
        return self._client._request("GET", self._path + "/" + _id(uuid) + "/ses-status")["data"]

    def dmarc(self, uuid):
        return self._client._request("GET", self._path + "/" + _id(uuid) + "/dmarc")["data"]

    def add_cloudflare_dns(self, uuid, body):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/dns/cloudflare", body)["data"]

    def setup_sending(self, uuid):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/setup-sending")["data"]

    def sending_status(self, uuid):
        return self._client._request("GET", self._path + "/" + _id(uuid) + "/sending-status")["data"]


class Identities(_CRUD):
    def __init__(self, client):
        super().__init__(client, "/identities")


class Triggers(_CRUD):
    def __init__(self, client):
        super().__init__(client, "/webhook-triggers")

    def test(self, uuid):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/test")["data"]

    def rotate_secret(self, uuid):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/rotate-secret")["data"]["secret"]


class Deliveries(_Read):
    def __init__(self, client):
        super().__init__(client, "/webhook-deliveries")

    def replay(self, uuid):
        return self._client._request("POST", self._path + "/" + _id(uuid) + "/replay")["data"]
