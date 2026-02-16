package controller

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
)

// SSEController handles Server-Sent Events for real-time notifications
type SSEController struct {
	clients     map[int64]map[string]chan *SSEEvent // userID -> clientID -> channel
	clientsLock sync.RWMutex
}

// SSEEvent represents an event to be sent to clients
type SSEEvent struct {
	Type       string      `json:"type"`
	Data       interface{} `json:"data"`
	IdentityID int64       `json:"identityId,omitempty"`
}

// NewSSEController creates a new SSE controller
func NewSSEController() *SSEController {
	return &SSEController{
		clients: make(map[int64]map[string]chan *SSEEvent),
	}
}

// Connect handles SSE connection requests
// GET /api/v1/sse/connect
func (c *SSEController) Connect(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		r.Response.WriteStatus(401)
		r.Response.Write("Unauthorized")
		return
	}

	// Set SSE headers
	r.Response.Header().Set("Content-Type", "text/event-stream")
	r.Response.Header().Set("Cache-Control", "no-cache")
	r.Response.Header().Set("Connection", "keep-alive")
	r.Response.Header().Set("Access-Control-Allow-Origin", "*")
	r.Response.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	// Create client channel
	clientID := fmt.Sprintf("%d-%d", claims.UserID, time.Now().UnixNano())
	eventChan := make(chan *SSEEvent, 100)

	// Register client
	c.registerClient(claims.UserID, clientID, eventChan)
	defer c.unregisterClient(claims.UserID, clientID)

	g.Log().Infof(r.Context(), "SSE client connected: user=%d, client=%s", claims.UserID, clientID)

	// Send initial connection event
	c.writeEvent(r, &SSEEvent{
		Type: "connected",
		Data: map[string]interface{}{
			"clientId":  clientID,
			"timestamp": time.Now().Format(time.RFC3339),
		},
	})

	// Heartbeat ticker
	heartbeat := time.NewTicker(30 * time.Second)
	defer heartbeat.Stop()

	// Keep connection open
	for {
		select {
		case event := <-eventChan:
			c.writeEvent(r, event)
		case <-heartbeat.C:
			// Send heartbeat
			c.writeEvent(r, &SSEEvent{
				Type: "heartbeat",
				Data: map[string]interface{}{
					"timestamp": time.Now().Format(time.RFC3339),
				},
			})
		case <-r.Context().Done():
			g.Log().Infof(r.Context(), "SSE client disconnected: user=%d, client=%s", claims.UserID, clientID)
			return
		}
	}
}

// writeEvent writes an SSE event to the response
func (c *SSEController) writeEvent(r *ghttp.Request, event *SSEEvent) {
	data, err := json.Marshal(event)
	if err != nil {
		return
	}

	// SSE format: "event: type\ndata: json\n\n"
	r.Response.Writef("event: %s\n", event.Type)
	r.Response.Writef("data: %s\n\n", string(data))
	r.Response.Flush()
}

// registerClient adds a client to the registry
func (c *SSEController) registerClient(userID int64, clientID string, eventChan chan *SSEEvent) {
	c.clientsLock.Lock()
	defer c.clientsLock.Unlock()

	if c.clients[userID] == nil {
		c.clients[userID] = make(map[string]chan *SSEEvent)
	}
	c.clients[userID][clientID] = eventChan
}

// unregisterClient removes a client from the registry
func (c *SSEController) unregisterClient(userID int64, clientID string) {
	c.clientsLock.Lock()
	defer c.clientsLock.Unlock()

	if userClients, ok := c.clients[userID]; ok {
		if ch, ok := userClients[clientID]; ok {
			close(ch)
			delete(userClients, clientID)
		}
		if len(userClients) == 0 {
			delete(c.clients, userID)
		}
	}
}

// NotifyUser sends an event to all connected clients of a user
func (c *SSEController) NotifyUser(userID int64, event *SSEEvent) {
	c.clientsLock.RLock()
	defer c.clientsLock.RUnlock()

	if userClients, ok := c.clients[userID]; ok {
		for _, ch := range userClients {
			select {
			case ch <- event:
			default:
				// Channel full, skip
			}
		}
	}
}

// NotifyNewEmail notifies a user about a new received email
func (c *SSEController) NotifyNewEmail(userID int64, email *model.ReceivedEmail) {
	c.NotifyUser(userID, &SSEEvent{
		Type:       "new_email",
		IdentityID: email.IdentityID,
		Data: map[string]interface{}{
			"id":             email.ID,
			"uuid":           email.UUID,
			"identityId":     email.IdentityID,
			"fromEmail":      email.FromEmail,
			"fromName":       email.FromName,
			"subject":        email.Subject,
			"snippet":        email.Snippet,
			"receivedAt":     email.ReceivedAt.Format(time.RFC3339),
			"hasAttachments": email.HasAttachments,
		},
	})
}

// NotifyEmailUpdate notifies about an email status update
func (c *SSEController) NotifyEmailUpdate(userID int64, emailUUID string, updates map[string]interface{}) {
	c.NotifyUser(userID, &SSEEvent{
		Type: "email_update",
		Data: map[string]interface{}{
			"uuid":    emailUUID,
			"updates": updates,
		},
	})
}

// NotifyEmailDeleted notifies about email deletion
func (c *SSEController) NotifyEmailDeleted(userID int64, emailUUIDs []string) {
	c.NotifyUser(userID, &SSEEvent{
		Type: "email_deleted",
		Data: map[string]interface{}{
			"uuids": emailUUIDs,
		},
	})
}

// NotifyCountsUpdate notifies about inbox counts update
func (c *SSEController) NotifyCountsUpdate(userID int64, identityID int64, counts *model.InboxCountsResponse) {
	c.NotifyUser(userID, &SSEEvent{
		Type:       "counts_update",
		IdentityID: identityID,
		Data:       counts,
	})
}

// GetConnectedCount returns the number of connected clients for a user
func (c *SSEController) GetConnectedCount(userID int64) int {
	c.clientsLock.RLock()
	defer c.clientsLock.RUnlock()

	if userClients, ok := c.clients[userID]; ok {
		return len(userClients)
	}
	return 0
}

// GetTotalConnections returns the total number of connected clients
func (c *SSEController) GetTotalConnections() int {
	c.clientsLock.RLock()
	defer c.clientsLock.RUnlock()

	total := 0
	for _, userClients := range c.clients {
		total += len(userClients)
	}
	return total
}

// Broadcast sends an event to all connected clients
func (c *SSEController) Broadcast(event *SSEEvent) {
	c.clientsLock.RLock()
	defer c.clientsLock.RUnlock()

	for _, userClients := range c.clients {
		for _, ch := range userClients {
			select {
			case ch <- event:
			default:
				// Channel full, skip
			}
		}
	}
}
