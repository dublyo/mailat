package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// JMAPClient handles JMAP protocol communication with Stalwart
type JMAPClient struct {
	baseURL    string
	httpClient *http.Client
}

// JMAPSession represents the JMAP session response
type JMAPSession struct {
	Capabilities  map[string]interface{} `json:"capabilities"`
	Accounts      map[string]JMAPAccount `json:"accounts"`
	PrimaryAccounts map[string]string    `json:"primaryAccounts"`
	Username      string                 `json:"username"`
	APIUrl        string                 `json:"apiUrl"`
	DownloadUrl   string                 `json:"downloadUrl"`
	UploadUrl     string                 `json:"uploadUrl"`
	EventSourceUrl string                `json:"eventSourceUrl"`
	State         string                 `json:"state"`
}

type JMAPAccount struct {
	Name                   string `json:"name"`
	IsPersonal             bool   `json:"isPersonal"`
	IsReadOnly             bool   `json:"isReadOnly"`
	AccountCapabilities    map[string]interface{} `json:"accountCapabilities"`
}

// JMAPRequest represents a JMAP request
type JMAPRequest struct {
	Using       []string        `json:"using"`
	MethodCalls [][]interface{} `json:"methodCalls"`
}

// JMAPResponse represents a JMAP response
type JMAPResponse struct {
	MethodResponses [][]interface{}        `json:"methodResponses"`
	SessionState    string                 `json:"sessionState"`
}

// Mailbox represents a JMAP mailbox (folder)
type Mailbox struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	ParentID       *string `json:"parentId"`
	Role           *string `json:"role"` // inbox, drafts, sent, trash, junk, archive
	SortOrder      int     `json:"sortOrder"`
	TotalEmails    int     `json:"totalEmails"`
	UnreadEmails   int     `json:"unreadEmails"`
	TotalThreads   int     `json:"totalThreads"`
	UnreadThreads  int     `json:"unreadThreads"`
}

// Email represents a JMAP email
type Email struct {
	ID            string            `json:"id"`
	BlobID        string            `json:"blobId"`
	ThreadID      string            `json:"threadId"`
	MailboxIDs    map[string]bool   `json:"mailboxIds"`
	Keywords      map[string]bool   `json:"keywords"`
	Size          int               `json:"size"`
	ReceivedAt    time.Time         `json:"receivedAt"`
	MessageID     []string          `json:"messageId"`
	InReplyTo     []string          `json:"inReplyTo"`
	References    []string          `json:"references"`
	From          []EmailAddress    `json:"from"`
	To            []EmailAddress    `json:"to"`
	Cc            []EmailAddress    `json:"cc"`
	Bcc           []EmailAddress    `json:"bcc"`
	ReplyTo       []EmailAddress    `json:"replyTo"`
	Subject       string            `json:"subject"`
	SentAt        *time.Time        `json:"sentAt"`
	HasAttachment bool              `json:"hasAttachment"`
	Preview       string            `json:"preview"`
	BodyStructure *BodyPart         `json:"bodyStructure"`
	TextBody      []BodyPart        `json:"textBody"`
	HTMLBody      []BodyPart        `json:"htmlBody"`
	Attachments   []BodyPart        `json:"attachments"`
}

// EmailAddress represents a JMAP email address
type EmailAddress struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

// BodyPart represents a JMAP body part
type BodyPart struct {
	PartID      string   `json:"partId"`
	BlobID      string   `json:"blobId"`
	Size        int      `json:"size"`
	Name        *string  `json:"name"`
	Type        string   `json:"type"`
	Charset     *string  `json:"charset"`
	Disposition *string  `json:"disposition"`
	CID         *string  `json:"cid"`
	SubParts    []BodyPart `json:"subParts"`
}

// Thread represents a JMAP thread
type Thread struct {
	ID       string   `json:"id"`
	EmailIDs []string `json:"emailIds"`
}

// NewJMAPClient creates a new JMAP client
func NewJMAPClient(baseURL string) *JMAPClient {
	return &JMAPClient{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// GetSession gets the JMAP session for authentication
func (c *JMAPClient) GetSession(ctx context.Context, email, password string) (*JMAPSession, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+"/jmap/session", nil)
	if err != nil {
		return nil, err
	}

	auth := base64.StdEncoding.EncodeToString([]byte(email + ":" + password))
	req.Header.Set("Authorization", "Basic "+auth)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JMAP session request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("JMAP session failed with status %d: %s", resp.StatusCode, string(body))
	}

	var session JMAPSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("failed to decode JMAP session: %w", err)
	}

	return &session, nil
}

// Call executes a JMAP request
func (c *JMAPClient) Call(ctx context.Context, email, password string, request *JMAPRequest) (*JMAPResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/jmap", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	auth := base64.StdEncoding.EncodeToString([]byte(email + ":" + password))
	req.Header.Set("Authorization", "Basic "+auth)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("JMAP request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("JMAP call failed with status %d: %s", resp.StatusCode, string(bodyBytes))
	}

	var response JMAPResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode JMAP response: %w", err)
	}

	return &response, nil
}

// GetMailboxes retrieves all mailboxes for an account
func (c *JMAPClient) GetMailboxes(ctx context.Context, email, password, accountID string) ([]Mailbox, error) {
	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Mailbox/get",
				map[string]interface{}{
					"accountId": accountID,
				},
				"0",
			},
		},
	}

	response, err := c.Call(ctx, email, password, request)
	if err != nil {
		return nil, err
	}

	if len(response.MethodResponses) == 0 {
		return nil, fmt.Errorf("no response from JMAP")
	}

	// Parse the response
	methodResponse := response.MethodResponses[0]
	if len(methodResponse) < 2 {
		return nil, fmt.Errorf("invalid JMAP response format")
	}

	dataMap, ok := methodResponse[1].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid JMAP response data format")
	}

	listData, ok := dataMap["list"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid mailbox list format")
	}

	var mailboxes []Mailbox
	for _, item := range listData {
		mailboxData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		mailbox := Mailbox{}
		if id, ok := mailboxData["id"].(string); ok {
			mailbox.ID = id
		}
		if name, ok := mailboxData["name"].(string); ok {
			mailbox.Name = name
		}
		if parentID, ok := mailboxData["parentId"].(string); ok {
			mailbox.ParentID = &parentID
		}
		if role, ok := mailboxData["role"].(string); ok {
			mailbox.Role = &role
		}
		if sortOrder, ok := mailboxData["sortOrder"].(float64); ok {
			mailbox.SortOrder = int(sortOrder)
		}
		if totalEmails, ok := mailboxData["totalEmails"].(float64); ok {
			mailbox.TotalEmails = int(totalEmails)
		}
		if unreadEmails, ok := mailboxData["unreadEmails"].(float64); ok {
			mailbox.UnreadEmails = int(unreadEmails)
		}
		if totalThreads, ok := mailboxData["totalThreads"].(float64); ok {
			mailbox.TotalThreads = int(totalThreads)
		}
		if unreadThreads, ok := mailboxData["unreadThreads"].(float64); ok {
			mailbox.UnreadThreads = int(unreadThreads)
		}

		mailboxes = append(mailboxes, mailbox)
	}

	return mailboxes, nil
}

// GetEmails retrieves emails with specified properties
func (c *JMAPClient) GetEmails(ctx context.Context, email, password, accountID string, ids []string, properties []string) ([]Email, error) {
	if properties == nil {
		properties = []string{
			"id", "blobId", "threadId", "mailboxIds", "keywords", "size",
			"receivedAt", "messageId", "inReplyTo", "references",
			"from", "to", "cc", "bcc", "replyTo", "subject", "sentAt",
			"hasAttachment", "preview",
		}
	}

	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/get",
				map[string]interface{}{
					"accountId":  accountID,
					"ids":        ids,
					"properties": properties,
				},
				"0",
			},
		},
	}

	response, err := c.Call(ctx, email, password, request)
	if err != nil {
		return nil, err
	}

	return c.parseEmailsResponse(response)
}

// QueryEmails queries emails with a filter
func (c *JMAPClient) QueryEmails(ctx context.Context, email, password, accountID string, filter map[string]interface{}, sort []map[string]interface{}, position, limit int) ([]string, int, error) {
	if sort == nil {
		sort = []map[string]interface{}{
			{"property": "receivedAt", "isAscending": false},
		}
	}

	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/query",
				map[string]interface{}{
					"accountId": accountID,
					"filter":    filter,
					"sort":      sort,
					"position":  position,
					"limit":     limit,
				},
				"0",
			},
		},
	}

	response, err := c.Call(ctx, email, password, request)
	if err != nil {
		return nil, 0, err
	}

	if len(response.MethodResponses) == 0 {
		return nil, 0, fmt.Errorf("no response from JMAP")
	}

	methodResponse := response.MethodResponses[0]
	if len(methodResponse) < 2 {
		return nil, 0, fmt.Errorf("invalid JMAP response format")
	}

	dataMap, ok := methodResponse[1].(map[string]interface{})
	if !ok {
		return nil, 0, fmt.Errorf("invalid JMAP response data format")
	}

	var ids []string
	if idsData, ok := dataMap["ids"].([]interface{}); ok {
		for _, id := range idsData {
			if idStr, ok := id.(string); ok {
				ids = append(ids, idStr)
			}
		}
	}

	total := 0
	if totalData, ok := dataMap["total"].(float64); ok {
		total = int(totalData)
	}

	return ids, total, nil
}

// QueryAndGetEmails queries and fetches emails in one request
func (c *JMAPClient) QueryAndGetEmails(ctx context.Context, email, password, accountID string, filter map[string]interface{}, sort []map[string]interface{}, position, limit int, properties []string) ([]Email, int, error) {
	if sort == nil {
		sort = []map[string]interface{}{
			{"property": "receivedAt", "isAscending": false},
		}
	}
	if properties == nil {
		properties = []string{
			"id", "blobId", "threadId", "mailboxIds", "keywords", "size",
			"receivedAt", "messageId", "inReplyTo", "references",
			"from", "to", "cc", "bcc", "replyTo", "subject", "sentAt",
			"hasAttachment", "preview",
		}
	}

	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/query",
				map[string]interface{}{
					"accountId": accountID,
					"filter":    filter,
					"sort":      sort,
					"position":  position,
					"limit":     limit,
				},
				"0",
			},
			{
				"Email/get",
				map[string]interface{}{
					"accountId":  accountID,
					"#ids": map[string]interface{}{
						"resultOf": "0",
						"name":     "Email/query",
						"path":     "/ids",
					},
					"properties": properties,
				},
				"1",
			},
		},
	}

	response, err := c.Call(ctx, email, password, request)
	if err != nil {
		return nil, 0, err
	}

	// Get total from query response
	total := 0
	if len(response.MethodResponses) > 0 {
		if dataMap, ok := response.MethodResponses[0][1].(map[string]interface{}); ok {
			if totalData, ok := dataMap["total"].(float64); ok {
				total = int(totalData)
			}
		}
	}

	// Parse emails from get response
	if len(response.MethodResponses) < 2 {
		return []Email{}, total, nil
	}

	// Create a modified response for parsing
	emailResponse := &JMAPResponse{
		MethodResponses: [][]interface{}{response.MethodResponses[1]},
	}

	emails, err := c.parseEmailsResponse(emailResponse)
	if err != nil {
		return nil, 0, err
	}

	return emails, total, nil
}

// SetEmailKeywords sets keywords (flags) on emails
func (c *JMAPClient) SetEmailKeywords(ctx context.Context, email, password, accountID string, updates map[string]map[string]interface{}) error {
	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Email/set",
				map[string]interface{}{
					"accountId": accountID,
					"update":    updates,
				},
				"0",
			},
		},
	}

	response, err := c.Call(ctx, email, password, request)
	if err != nil {
		return err
	}

	// Check for errors
	if len(response.MethodResponses) > 0 {
		if dataMap, ok := response.MethodResponses[0][1].(map[string]interface{}); ok {
			if notUpdated, ok := dataMap["notUpdated"].(map[string]interface{}); ok && len(notUpdated) > 0 {
				return fmt.Errorf("failed to update some emails: %v", notUpdated)
			}
		}
	}

	return nil
}

// MoveEmails moves emails to different mailboxes
func (c *JMAPClient) MoveEmails(ctx context.Context, email, password, accountID string, emailIDs []string, fromMailboxID, toMailboxID string) error {
	updates := make(map[string]map[string]interface{})
	for _, id := range emailIDs {
		updates[id] = map[string]interface{}{
			"mailboxIds/" + fromMailboxID: nil,
			"mailboxIds/" + toMailboxID:   true,
		}
	}

	return c.SetEmailKeywords(ctx, email, password, accountID, updates)
}

// DeleteEmails moves emails to trash or permanently deletes them
func (c *JMAPClient) DeleteEmails(ctx context.Context, email, password, accountID string, emailIDs []string, permanent bool) error {
	if permanent {
		request := &JMAPRequest{
			Using: []string{
				"urn:ietf:params:jmap:core",
				"urn:ietf:params:jmap:mail",
			},
			MethodCalls: [][]interface{}{
				{
					"Email/set",
					map[string]interface{}{
						"accountId": accountID,
						"destroy":   emailIDs,
					},
					"0",
				},
			},
		}

		_, err := c.Call(ctx, email, password, request)
		return err
	}

	// Move to trash - we need to find the trash mailbox first
	mailboxes, err := c.GetMailboxes(ctx, email, password, accountID)
	if err != nil {
		return err
	}

	var trashID string
	for _, mb := range mailboxes {
		if mb.Role != nil && *mb.Role == "trash" {
			trashID = mb.ID
			break
		}
	}

	if trashID == "" {
		return fmt.Errorf("trash mailbox not found")
	}

	// Mark as deleted and move to trash
	updates := make(map[string]map[string]interface{})
	for _, id := range emailIDs {
		updates[id] = map[string]interface{}{
			"mailboxIds": map[string]bool{trashID: true},
		}
	}

	return c.SetEmailKeywords(ctx, email, password, accountID, updates)
}

// GetThreads retrieves threads by IDs
func (c *JMAPClient) GetThreads(ctx context.Context, email, password, accountID string, ids []string) ([]Thread, error) {
	request := &JMAPRequest{
		Using: []string{
			"urn:ietf:params:jmap:core",
			"urn:ietf:params:jmap:mail",
		},
		MethodCalls: [][]interface{}{
			{
				"Thread/get",
				map[string]interface{}{
					"accountId": accountID,
					"ids":       ids,
				},
				"0",
			},
		},
	}

	response, err := c.Call(ctx, email, password, request)
	if err != nil {
		return nil, err
	}

	if len(response.MethodResponses) == 0 {
		return nil, fmt.Errorf("no response from JMAP")
	}

	methodResponse := response.MethodResponses[0]
	if len(methodResponse) < 2 {
		return nil, fmt.Errorf("invalid JMAP response format")
	}

	dataMap, ok := methodResponse[1].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid JMAP response data format")
	}

	listData, ok := dataMap["list"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid thread list format")
	}

	var threads []Thread
	for _, item := range listData {
		threadData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		thread := Thread{}
		if id, ok := threadData["id"].(string); ok {
			thread.ID = id
		}
		if emailIDs, ok := threadData["emailIds"].([]interface{}); ok {
			for _, eid := range emailIDs {
				if eidStr, ok := eid.(string); ok {
					thread.EmailIDs = append(thread.EmailIDs, eidStr)
				}
			}
		}

		threads = append(threads, thread)
	}

	return threads, nil
}

// parseEmailsResponse parses emails from a JMAP response
func (c *JMAPClient) parseEmailsResponse(response *JMAPResponse) ([]Email, error) {
	if len(response.MethodResponses) == 0 {
		return nil, fmt.Errorf("no response from JMAP")
	}

	methodResponse := response.MethodResponses[0]
	if len(methodResponse) < 2 {
		return nil, fmt.Errorf("invalid JMAP response format")
	}

	dataMap, ok := methodResponse[1].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid JMAP response data format")
	}

	listData, ok := dataMap["list"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("invalid email list format")
	}

	var emails []Email
	for _, item := range listData {
		emailData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		email := Email{}
		if id, ok := emailData["id"].(string); ok {
			email.ID = id
		}
		if blobID, ok := emailData["blobId"].(string); ok {
			email.BlobID = blobID
		}
		if threadID, ok := emailData["threadId"].(string); ok {
			email.ThreadID = threadID
		}
		if subject, ok := emailData["subject"].(string); ok {
			email.Subject = subject
		}
		if preview, ok := emailData["preview"].(string); ok {
			email.Preview = preview
		}
		if size, ok := emailData["size"].(float64); ok {
			email.Size = int(size)
		}
		if hasAttachment, ok := emailData["hasAttachment"].(bool); ok {
			email.HasAttachment = hasAttachment
		}

		// Parse receivedAt
		if receivedAt, ok := emailData["receivedAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339, receivedAt); err == nil {
				email.ReceivedAt = t
			}
		}

		// Parse sentAt
		if sentAt, ok := emailData["sentAt"].(string); ok {
			if t, err := time.Parse(time.RFC3339, sentAt); err == nil {
				email.SentAt = &t
			}
		}

		// Parse mailboxIds
		if mailboxIDs, ok := emailData["mailboxIds"].(map[string]interface{}); ok {
			email.MailboxIDs = make(map[string]bool)
			for k, v := range mailboxIDs {
				if b, ok := v.(bool); ok {
					email.MailboxIDs[k] = b
				}
			}
		}

		// Parse keywords
		if keywords, ok := emailData["keywords"].(map[string]interface{}); ok {
			email.Keywords = make(map[string]bool)
			for k, v := range keywords {
				if b, ok := v.(bool); ok {
					email.Keywords[k] = b
				}
			}
		}

		// Parse from addresses
		if from, ok := emailData["from"].([]interface{}); ok {
			email.From = parseEmailAddresses(from)
		}

		// Parse to addresses
		if to, ok := emailData["to"].([]interface{}); ok {
			email.To = parseEmailAddresses(to)
		}

		// Parse cc addresses
		if cc, ok := emailData["cc"].([]interface{}); ok {
			email.Cc = parseEmailAddresses(cc)
		}

		// Parse messageId
		if messageID, ok := emailData["messageId"].([]interface{}); ok {
			for _, mid := range messageID {
				if midStr, ok := mid.(string); ok {
					email.MessageID = append(email.MessageID, midStr)
				}
			}
		}

		// Parse inReplyTo
		if inReplyTo, ok := emailData["inReplyTo"].([]interface{}); ok {
			for _, irt := range inReplyTo {
				if irtStr, ok := irt.(string); ok {
					email.InReplyTo = append(email.InReplyTo, irtStr)
				}
			}
		}

		// Parse references
		if references, ok := emailData["references"].([]interface{}); ok {
			for _, ref := range references {
				if refStr, ok := ref.(string); ok {
					email.References = append(email.References, refStr)
				}
			}
		}

		emails = append(emails, email)
	}

	return emails, nil
}

// parseEmailAddresses parses email addresses from JMAP format
func parseEmailAddresses(data []interface{}) []EmailAddress {
	var addresses []EmailAddress
	for _, item := range data {
		addrData, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		addr := EmailAddress{}
		if name, ok := addrData["name"].(string); ok {
			addr.Name = name
		}
		if email, ok := addrData["email"].(string); ok {
			addr.Email = email
		}
		addresses = append(addresses, addr)
	}
	return addresses
}
