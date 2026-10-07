import test from 'node:test';
import assert from 'node:assert/strict';
import { createHmac } from 'node:crypto';
const { Mailat, MailatError } = await import(process.env.SDK_TEST_MODULE || '../dist/index.mjs');
const { DMARC_REPORTS_FOLDER } = await import(process.env.SDK_TEST_MODULE || '../dist/index.mjs');
const secret = 'test-secret-only';
const payload = JSON.stringify({version:'1',id:'event-1',type:'email.received',createdAt:'2026-10-04T00:00:00Z',data:{messageUuid:'mail-1',subject:'مرحبا'}});
const sign = (raw, time = Math.floor(Date.now()/1000)) => `t=${time},v1=${createHmac('sha256', secret).update(`${time}.`).update(raw).digest('hex')}`;

test('real HMAC verification rejects mutations, duplicate fields and replay timestamps', async () => {
 const signature = sign(payload);
 assert.equal(await Mailat.verifyWebhookSignature(payload,signature,secret),true);
 assert.equal(await Mailat.verifyWebhookSignature(new TextEncoder().encode(payload),signature,secret),true);
 for (const [raw,sig] of [[payload+' ',signature],[payload,signature+',t=1'],[payload,signature+',v1=00'],[payload,'x=1,'+signature],[payload,sign(payload,Math.floor(Date.now()/1000)-301)],[payload,sign(payload,Math.floor(Date.now()/1000)+301)]]) assert.equal(await Mailat.verifyWebhookSignature(raw,sig,secret),false);
 const claimed = new Set(); const claim = async id => { if(claimed.has(id))return false;claimed.add(id);return true; };
 assert.equal((await Mailat.parseWebhookPayload(payload,signature,secret,claim)).data.messageUuid,'mail-1');
 await assert.rejects(Mailat.parseWebhookPayload(payload,signature,secret,claim),e=>e.status===409);
});

test('DMARC folder counts retain global unread and use the existing move/list contracts', async () => {
 const calls=[]; const original=globalThis.fetch;
 const counts={inbox:5,inboxUnread:2,dmarcReports:3,dmarcReportsUnread:1,unread:7,starred:0,sent:0,drafts:0,spam:4,trash:0};
 globalThis.fetch=async(url,options)=>{calls.push({url,body:options.body&&JSON.parse(options.body)});return new Response(JSON.stringify({code:0,data:counts}));};
 try {
  const api=new Mailat({apiKey:'ue_test',baseUrl:'https://fixture.invalid/api/v1'});
  assert.deepEqual(await api.inbox.folderCounts(42),counts);
  assert.match(calls.at(-1).url,/counts\?identityId=42$/);
  assert.equal((await api.inbox.counts()).unread,7);
  await api.inbox.list({folder:DMARC_REPORTS_FOLDER,isRead:false});
  assert.match(calls.at(-1).url,/folder=dmarc-reports&isRead=false$/);
  await api.inbox.move(['message-1'],DMARC_REPORTS_FOLDER);
  assert.deepEqual(calls.at(-1).body,{emailUuids:['message-1'],folder:'dmarc-reports'});
 } finally {globalThis.fetch=original;}
});

test('HTTP resources preserve keys, camelCase, binary attachments and failure details', async () => {
 const calls=[]; const original=globalThis.fetch;
 globalThis.fetch=async (url,options)=>{
  const body=options.body?JSON.parse(options.body):undefined;calls.push({url,options,body});
  if(url.includes('/attachments/'))return new Response(new Uint8Array([0,255,1]));
  if(url.endsWith('/limited'))return new Response(JSON.stringify({code:429,message:'Request limit reached'}),{status:429,headers:{'Retry-After':'17'}});
  const data=url.endsWith('/emails')?{id:'mail-1',messageId:'ses-1',status:'sent',acceptedAt:'2026-10-04T00:00:00Z'}:url.endsWith('/emails/batch')?{results:[{index:0,id:'mail-1',messageId:'ses-1',status:'sent'}]}:url.endsWith('/templates')?[]:url.endsWith('/test')?{eventId:'event-1',deliveryId:'delivery-1',status:'retry',httpStatus:500}:{};
  return new Response(JSON.stringify({code:0,data}));
 };
 try{
 const api=new Mailat({apiKey:'ue_test',baseUrl:'https://fixture.invalid/api/v1/'});
 const email={from:'a@fixture.invalid',to:['b@fixture.invalid'],subject:'hello',text:'body',attachments:[{name:'test.txt',content:'YQ==',type:'text/plain'}]};
 await assert.rejects(api.emails.send(email),/idempotency/);
 const result=await api.emails.send(email,{idempotencyKey:'same-send-key'});assert.equal(result.messageId,'ses-1');
 assert.equal(calls.at(-1).options.headers['Idempotency-Key'],'same-send-key');assert.equal(calls.at(-1).body.attachments[0].type,'text/plain');
 await api.emails.sendBatch([email],{idempotencyKey:'batch-send-key'});assert.equal(calls.at(-1).options.headers['Idempotency-Key'],'batch-send-key');
 assert.deepEqual(await api.templates.list(),[]);
 await api.inbox.mark(['mail-1'],false);assert.deepEqual(calls.at(-1).body,{emailUuids:['mail-1'],isRead:false});
 await api.inbox.assignLabels(['mail-1'],['Invoices'],['Old']);assert.deepEqual(calls.at(-1).body,{emailUuids:['mail-1'],addLabels:['Invoices'],removeLabels:['Old']});
 await api.inbox.changes('cursor+/?',25);assert.match(calls.at(-1).url,/cursor=cursor%2B%2F%3F/);
 assert.deepEqual([...await api.inbox.attachment('mail-1','attachment-1')],[0,255,1]);
 await api.domains.setupSending('domain-1');assert.match(calls.at(-1).url,/domains\/domain-1\/setup-sending$/);
 await api.identities.create({domainId:'domain-1',email:'hello@fixture.invalid',displayName:'Hello'});
 assert.equal((await api.triggers.test('trigger-1')).httpStatus,500);
 await api.deliveries.replay('delivery-1');assert.match(calls.at(-1).url,/webhook-deliveries\/delivery-1\/replay$/);
 await assert.rejects(api.inbox.get('limited'),e=>e instanceof MailatError&&e.retryAfter==='17'&&e.code===429);
 }finally{globalThis.fetch=original;}
});

test('baseUrl is required and normalised to exactly one /api/v1 suffix', async () => {
 assert.throws(()=>new Mailat({apiKey:'ue_test'}),/baseUrl is required \(e\.g\. https:\/\/mail\.example\.com\)/);
 assert.throws(()=>new Mailat({apiKey:'ue_test',baseUrl:'  '}),/baseUrl is required/);
 assert.throws(()=>new Mailat({apiKey:'ue_test',baseUrl:'mail.example.com'}),/not a valid URL/);
 assert.throws(()=>new Mailat({apiKey:'ue_test',baseUrl:'ftp://x'}),/http\(s\)/);
 const calls=[]; const original=globalThis.fetch;
 globalThis.fetch=async url=>{calls.push(url);return new Response(JSON.stringify({code:0,data:[]}));};
 try {
  for (const baseUrl of ['https://x','https://x/','https://x/api/v1','https://x/api/v1/']) {
   await new Mailat({apiKey:'ue_test',baseUrl}).templates.list();
   assert.equal(calls.at(-1),'https://x/api/v1/templates');
  }
  await new Mailat({apiKey:'ue_test',baseUrl:'https://x/mailat/'}).templates.list();
  assert.equal(calls.at(-1),'https://x/mailat/api/v1/templates');
 } finally {globalThis.fetch=original;}
});
