package mailat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

// Object retains documented camelCase keys for evolving inbox/domain resources.
type Object map[string]interface{}

func sendHeaders(key string) (map[string]string, error) {
	if len(key) < 8 || len(key) > 128 {
		return nil, fmt.Errorf("a stable 8–128 character idempotency key is required")
	}
	return map[string]string{"Idempotency-Key": key}, nil
}
func query(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	return "?" + values.Encode()
}
func object(c *Client, ctx context.Context, method, path string, body interface{}, headers map[string]string) (Object, error) {
	data, err := c.request(ctx, method, path, body, headers)
	if err != nil {
		return nil, err
	}
	var out Object
	if string(data) == "null" || len(data) == 0 {
		return Object{}, nil
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

type ReadResource struct {
	client *Client
	path   string
}

// List returns the resource's data value (array or paginated object), preserving its wire shape.
func (s *ReadResource) List(ctx context.Context, options url.Values) (json.RawMessage, error) {
	return s.client.request(ctx, "GET", s.path+query(options), nil, nil)
}
func (s *ReadResource) Get(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "GET", s.path+"/"+url.PathEscape(id), nil, nil)
}

type CRUDResource struct{ ReadResource }

func (s *CRUDResource) Create(ctx context.Context, body Object) (Object, error) {
	return object(s.client, ctx, "POST", s.path, body, nil)
}
func (s *CRUDResource) Update(ctx context.Context, id string, body Object) (Object, error) {
	return object(s.client, ctx, "PUT", s.path+"/"+url.PathEscape(id), body, nil)
}
func (s *CRUDResource) Delete(ctx context.Context, id string) error {
	_, err := s.client.request(ctx, "DELETE", s.path+"/"+url.PathEscape(id), nil, nil)
	return err
}

type LabelsService struct{ client *Client }

func (s *LabelsService) List(ctx context.Context) ([]Object, error) {
	data, err := s.client.request(ctx, "GET", "/inbox/labels", nil, nil)
	if err != nil {
		return nil, err
	}
	out := []Object{}
	err = json.Unmarshal(data, &out)
	if out == nil {
		out = []Object{}
	}
	return out, err
}
func (s *LabelsService) Create(ctx context.Context, body Object) (Object, error) {
	return object(s.client, ctx, "POST", "/inbox/labels", body, nil)
}
func (s *LabelsService) Update(ctx context.Context, id string, body Object) (Object, error) {
	return object(s.client, ctx, "PUT", "/inbox/labels/"+url.PathEscape(id), body, nil)
}
func (s *LabelsService) Delete(ctx context.Context, id string) error {
	_, err := s.client.request(ctx, "DELETE", "/inbox/labels/"+url.PathEscape(id), nil, nil)
	return err
}

type InboxService struct {
	ReadResource
	Labels  *LabelsService
	Filters *CRUDResource
}

func newInbox(c *Client) *InboxService {
	return &InboxService{ReadResource{c, "/inbox/received"}, &LabelsService{c}, &CRUDResource{ReadResource{c, "/inbox/filters"}}}
}
func (s *InboxService) Changes(ctx context.Context, cursor string, limit int) (Object, error) {
	return object(s.client, ctx, "GET", "/inbox/changes"+query(url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}), nil, nil)
}
func (s *InboxService) Attachment(ctx context.Context, messageID, attachmentID string) ([]byte, error) {
	return s.client.request(ctx, "GET", s.path+"/"+url.PathEscape(messageID)+"/attachments/"+url.PathEscape(attachmentID), nil, nil, true)
}
func (s *InboxService) TestFilter(ctx context.Context, id string, body Object) (Object, error) {
	return object(s.client, ctx, "POST", "/inbox/filters/"+url.PathEscape(id)+"/test", body, nil)
}
func (s *InboxService) Mark(ctx context.Context, ids []string, value bool) error {
	_, err := s.client.request(ctx, "POST", s.path+"/mark", Object{"emailUuids": ids, "isRead": value}, nil)
	return err
}
func (s *InboxService) Star(ctx context.Context, ids []string, value bool) error {
	_, err := s.client.request(ctx, "POST", s.path+"/star", Object{"emailUuids": ids, "isStarred": value}, nil)
	return err
}
func (s *InboxService) Move(ctx context.Context, ids []string, value string) error {
	_, err := s.client.request(ctx, "POST", s.path+"/move", Object{"emailUuids": ids, "folder": value}, nil)
	return err
}
func (s *InboxService) Trash(ctx context.Context, ids []string, value bool) error {
	_, err := s.client.request(ctx, "POST", s.path+"/trash", Object{"emailUuids": ids, "permanent": value}, nil)
	return err
}
func (s *InboxService) AssignLabels(ctx context.Context, ids []string, addLabels []string, removeLabels []string) error {
	_, err := s.client.request(ctx, "POST", s.path+"/labels", Object{"emailUuids": ids, "addLabels": addLabels, "removeLabels": removeLabels}, nil)
	return err
}

type ComposeService struct{ client *Client }

func (s *ComposeService) ReplyContext(ctx context.Context, id string, replyAll bool) (Object, error) {
	return object(s.client, ctx, "GET", "/compose/reply/"+url.PathEscape(id)+"?replyAll="+strconv.FormatBool(replyAll), nil, nil)
}
func (s *ComposeService) ForwardContext(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "GET", "/compose/forward/"+url.PathEscape(id), nil, nil)
}
func (s *ComposeService) Send(ctx context.Context, body Object, key string) (Object, error) {
	headers, err := sendHeaders(key)
	if err != nil {
		return nil, err
	}
	return object(s.client, ctx, "POST", "/compose/send", body, headers)
}
func (s *ComposeService) SaveDraft(ctx context.Context, body Object) (Object, error) {
	return object(s.client, ctx, "POST", "/compose/drafts", body, nil)
}
func (s *ComposeService) UpdateDraft(ctx context.Context, id string, body Object) (Object, error) {
	return object(s.client, ctx, "PUT", "/compose/drafts/"+url.PathEscape(id), body, nil)
}
func (s *ComposeService) DeleteDraft(ctx context.Context, id string) error {
	_, err := s.client.request(ctx, "DELETE", "/compose/drafts/"+url.PathEscape(id), nil, nil)
	return err
}

type DomainsService struct{ ReadResource }

func (s *DomainsService) Create(ctx context.Context, body Object) (Object, error) {
	return object(s.client, ctx, "POST", s.path, body, nil)
}
func (s *DomainsService) Delete(ctx context.Context, id string) error {
	_, err := s.client.request(ctx, "DELETE", s.path+"/"+url.PathEscape(id), nil, nil)
	return err
}
func (s *DomainsService) Verify(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "POST", s.path+"/"+url.PathEscape(id)+"/verify", nil, nil)
}
func (s *DomainsService) SetupSending(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "POST", s.path+"/"+url.PathEscape(id)+"/setup-sending", nil, nil)
}
func (s *DomainsService) SendingStatus(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "GET", s.path+"/"+url.PathEscape(id)+"/sending-status", nil, nil)
}

type TriggersService struct{ CRUDResource }

func (s *TriggersService) Test(ctx context.Context, id string) (*WebhookTestResult, error) {
	data, err := s.client.request(ctx, "POST", s.path+"/"+url.PathEscape(id)+"/test", nil, nil)
	if err != nil {
		return nil, err
	}
	var result WebhookTestResult
	err = json.Unmarshal(data, &result)
	return &result, err
}
func (s *TriggersService) RotateSecret(ctx context.Context, id string) (string, error) {
	result, err := object(s.client, ctx, "POST", s.path+"/"+url.PathEscape(id)+"/rotate-secret", nil, nil)
	if err != nil {
		return "", err
	}
	secret, _ := result["secret"].(string)
	return secret, nil
}

type DeliveriesService struct{ ReadResource }

func (s *DeliveriesService) Replay(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "POST", s.path+"/"+url.PathEscape(id)+"/replay", nil, nil)
}

func (s *InboxService) Counts(ctx context.Context) (Object, error) {
	return object(s.client, ctx, "GET", s.path+"/counts", nil, nil)
}

// FolderCounts is a typed alternative to Counts. Identity 0 selects all owned identities.
func (s *InboxService) FolderCounts(ctx context.Context, identityID int64) (*InboxCounts, error) {
	values := url.Values{}
	if identityID > 0 {
		values.Set("identityId", strconv.FormatInt(identityID, 10))
	}
	data, err := s.client.request(ctx, "GET", s.path+"/counts"+query(values), nil, nil)
	if err != nil {
		return nil, err
	}
	var counts InboxCounts
	err = json.Unmarshal(data, &counts)
	return &counts, err
}
func (s *InboxService) SetupReceiving(ctx context.Context, domainID int64) (Object, error) {
	return object(s.client, ctx, "POST", "/inbox/setup", Object{"domainId": domainID}, nil)
}
func (s *DomainsService) InitiateSES(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "POST", s.path+"/"+url.PathEscape(id)+"/ses-verify", nil, nil)
}
func (s *DomainsService) SESStatus(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "GET", s.path+"/"+url.PathEscape(id)+"/ses-status", nil, nil)
}
func (s *DomainsService) DMARC(ctx context.Context, id string) (Object, error) {
	return object(s.client, ctx, "GET", s.path+"/"+url.PathEscape(id)+"/dmarc", nil, nil)
}
func (s *DomainsService) AddCloudflareDNS(ctx context.Context, id string, body Object) (Object, error) {
	return object(s.client, ctx, "POST", s.path+"/"+url.PathEscape(id)+"/dns/cloudflare", body, nil)
}
