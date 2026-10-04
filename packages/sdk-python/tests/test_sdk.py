import hashlib
import hmac
import json
import time
import unittest
import httpx
from mailat import Mailat, SendEmailRequest, MailatError
from mailat.models import Attachment, EmailStatusResponse, Webhook
from mailat import DMARC_REPORTS_FOLDER, DMARCReportsSettings, UpdateDMARCReportsSettings

SECRET = "fixture-secret"
PAYLOAD = json.dumps({"version":"1","id":"event-1","type":"email.received","createdAt":"2026-10-04T00:00:00Z","data":{"messageUuid":"mail-1","subject":"مرحبا"}}, ensure_ascii=False).encode()
def sign(raw, stamp=None):
    stamp = str(int(time.time()) if stamp is None else stamp)
    return "t=" + stamp + ",v1=" + hmac.new(SECRET.encode(), stamp.encode() + b"." + raw, hashlib.sha256).hexdigest()

class SDKTests(unittest.TestCase):
    def test_dmarc_folder_counts_and_preference(self):
        calls=[]
        counts={"inbox":5,"inboxUnread":2,"dmarcReports":3,"dmarcReportsUnread":1,"unread":7,"starred":0,"sent":0,"drafts":0,"spam":4,"trash":0}
        def handler(request):
            calls.append(request)
            return httpx.Response(200,json={"code":0,"data":counts})
        client=Mailat("ue_fixture",base_url="https://fixture.invalid/api/v1")
        client._client.close();client._client=httpx.Client(transport=httpx.MockTransport(handler))
        with client:
            result=client.inbox.folder_counts(identity_id=42)
            self.assertEqual((result.inbox_unread,result.dmarc_reports,result.dmarc_reports_unread,result.unread),(2,3,1,7))
            self.assertEqual(calls[-1].url.params["identityId"],"42")
            self.assertEqual(client.inbox.counts(),counts)
            client.inbox.list(folder=DMARC_REPORTS_FOLDER,isRead=False)
            self.assertEqual(calls[-1].url.params["folder"],"dmarc-reports")
            self.assertEqual(calls[-1].url.params["isRead"],"false")
            client.inbox.move(["message-1"],DMARC_REPORTS_FOLDER)
            self.assertEqual(json.loads(calls[-1].content),{"emailUuids":["message-1"],"folder":"dmarc-reports"})
        self.assertTrue(DMARCReportsSettings(autoOrganizeDmarcReports=True).auto_organize_dmarc_reports)
        self.assertEqual(UpdateDMARCReportsSettings().model_dump(by_alias=True,exclude_none=True),{})
        self.assertEqual(UpdateDMARCReportsSettings(auto_organize_dmarc_reports=False).model_dump(by_alias=True,exclude_none=True),{"autoOrganizeDmarcReports":False})

    def test_signature_and_claim(self):
        signature = sign(PAYLOAD)
        self.assertTrue(Mailat.verify_webhook_signature(PAYLOAD,signature,SECRET))
        for raw,sig in [(PAYLOAD+b" ",signature),(PAYLOAD,signature+",t=1"),(PAYLOAD,"t=,"+signature),(PAYLOAD,"x=1,"+signature),(PAYLOAD,sign(PAYLOAD,int(time.time())-301)),(PAYLOAD,sign(PAYLOAD,int(time.time())+301))]:
            self.assertFalse(Mailat.verify_webhook_signature(raw,sig,SECRET))
        seen=set()
        def claim(event_id):
            if event_id in seen:return False
            seen.add(event_id);return True
        event=Mailat.parse_webhook_payload(PAYLOAD,signature,SECRET,claim)
        self.assertEqual(event.created_at.year,2026)
        with self.assertRaises(MailatError):Mailat.parse_webhook_payload(PAYLOAD,signature,SECRET,claim)

    def test_wire_contract(self):
        calls=[]
        def handler(request):
            body=json.loads(request.content) if request.content else None
            calls.append((request,body))
            path=request.url.path
            if "/attachments/" in path:return httpx.Response(200,content=b"\x00\xff\x01")
            if path.endswith("/limited"):return httpx.Response(429,json={"code":429,"message":"rate limited"},headers={"Retry-After":"12"})
            data={}
            if path.endswith("/emails"):data={"id":"mail-1","messageId":"ses-1","status":"sent","acceptedAt":"2026-10-04T00:00:00Z"}
            if path.endswith("/batch"):data={"results":[{"index":0,"id":"mail-1","messageId":"ses-1","status":"sent"}]}
            if path.endswith("/templates"):data=None
            if path.endswith("/test"):data={"eventId":"event-1","deliveryId":"delivery-1","status":"retry","httpStatus":500}
            return httpx.Response(200,json={"code":0,"data":data})
        client=Mailat("ue_test",base_url="https://fixture.invalid/api/v1/")
        client._client.close();client._client=httpx.Client(transport=httpx.MockTransport(handler))
        with client:
            result=client.emails.send("a@fixture.invalid",["b@fixture.invalid"],"Hi",text="body",idempotency_key="same-send-key",attachments=[Attachment(name="test.txt",content="YQ==",type="text/plain")])
            self.assertEqual(result.message_id,"ses-1");self.assertEqual(calls[-1][0].headers["Idempotency-Key"],"same-send-key");self.assertEqual(calls[-1][1]["attachments"][0]["type"],"text/plain")
            email=SendEmailRequest(from_address="a@fixture.invalid",to=["b@fixture.invalid"],subject="Hi",reply_to="c@fixture.invalid",idempotency_key="item-send-key")
            client.emails.send_batch([email],idempotency_key="batch-send-key")
            self.assertEqual(calls[-1][1]["emails"][0]["replyTo"],"c@fixture.invalid");self.assertEqual(calls[-1][0].headers["Idempotency-Key"],"batch-send-key")
            self.assertEqual(client.templates.list(),[])
            client.inbox.mark(["mail-1"],False);self.assertEqual(calls[-1][1],{"emailUuids":["mail-1"],"isRead":False})
            client.inbox.assign_labels(["mail-1"],["Invoices"],["Old"]);self.assertEqual(calls[-1][1],{"emailUuids":["mail-1"],"addLabels":["Invoices"],"removeLabels":["Old"]})
            self.assertEqual(client.inbox.attachment("mail-1","attachment-1"),b"\x00\xff\x01")
            client.domains.setup_sending("domain-1");self.assertTrue(calls[-1][0].url.path.endswith("/setup-sending"))
            self.assertEqual(client.triggers.test("trigger-1")["httpStatus"],500)
            with self.assertRaises(MailatError) as error:client.inbox.get("limited")
            self.assertEqual(error.exception.retry_after,"12")
        status=EmailStatusResponse(id="mail-1",messageId="ses-1",from_address="a@fixture.invalid",to=[],subject="",status="unknown",events=None,createdAt="2026-10-04T00:00:00Z")
        self.assertEqual(status.events,[])
        with self.assertRaises(ValueError):client.emails.send_batch([email])

if __name__ == "__main__":unittest.main()
