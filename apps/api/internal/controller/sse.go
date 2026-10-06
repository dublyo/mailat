package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"github.com/dublyo/mailat/api/internal/middleware"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
	"github.com/dublyo/mailat/api/pkg/response"
)

// Live updates replay the durable mailbox_changes feed. A wakeup (NOTIFY from
// any replica, or a fallback poll) only tells a stream to read the feed from its
// cursor, so a reconnecting client resumes exactly where it stopped.
const (
	sseUserCap         = 10
	sseDefaultMaxConns = 5000
	sseBatch           = 200
	sseResyncPending   = 500
	sseHeartbeat       = 25 * time.Second
	sseCredentialCheck = 60 * time.Second
	ssePollHealthy     = 60 * time.Second
	ssePollUnhealthy   = 5 * time.Second
	sseCountsDebounce  = 500 * time.Millisecond
	sseResyncExpired   = "expired"
	sseResyncAhead     = "ahead"
	sseResyncTooMany   = "too-many-changes"
)

// mailboxFeed is the part of InboxService the hub reads.
type mailboxFeed interface {
	Changes(ctx context.Context, userID int64, cursor string, limit int) (*service.MailboxChanges, error)
	MailboxSummaries(ctx context.Context, userID int64, uuids []string) (map[string]model.ReceivedEmail, error)
	GetReceivedEmailCounts(ctx context.Context, userID, identityID int64) (*model.InboxCountsResponse, error)
}

// mailboxWakeups delivers change hints; *service.MailboxNotifier implements it.
type mailboxWakeups interface {
	SetHandlers(onUser func(int64), onAll func())
	Healthy() bool
}

// SSEEvent is the JSON body of one stream event.
type SSEEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type sseClient struct {
	id      string
	userID  int64
	wake    chan struct{}
	evicted chan struct{}
	once    sync.Once
}

func (c *sseClient) evict() { c.once.Do(func() { close(c.evicted) }) }

func (c *sseClient) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// SSEController is the per-replica live-update hub.
type SSEController struct {
	feed     mailboxFeed
	wakeups  mailboxWakeups
	maxConns int
	seq      atomic.Int64

	mu    sync.Mutex
	users map[int64][]*sseClient // oldest first
	total int

	// Timings; tests shorten them.
	heartbeat, credentialCheck, pollHealthy, pollUnhealthy, countsDebounce time.Duration
}

func NewSSEController(wakeups mailboxWakeups, feed mailboxFeed, maxConns int) *SSEController {
	if maxConns <= 0 {
		maxConns = sseDefaultMaxConns
	}
	c := &SSEController{
		feed: feed, wakeups: wakeups, maxConns: maxConns, users: map[int64][]*sseClient{},
		heartbeat: sseHeartbeat, credentialCheck: sseCredentialCheck,
		pollHealthy: ssePollHealthy, pollUnhealthy: ssePollUnhealthy, countsDebounce: sseCountsDebounce,
	}
	if wakeups != nil {
		wakeups.SetHandlers(c.wakeUser, c.wakeAll)
	}
	return c
}

// register admits a stream. A user's eleventh stream closes their oldest; a
// full replica refuses new streams.
func (c *SSEController) register(userID int64) (*sseClient, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total >= c.maxConns {
		return nil, false
	}
	client := &sseClient{id: fmt.Sprintf("%d-%d", userID, c.seq.Add(1)), userID: userID, wake: make(chan struct{}, 1), evicted: make(chan struct{})}
	list := append(c.users[userID], client)
	for len(list) > sseUserCap {
		list[0].evict()
		list = list[1:]
		c.total--
	}
	c.users[userID] = list
	c.total++
	return client, true
}

func (c *SSEController) unregister(client *sseClient) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.users[client.userID]
	for i, other := range list {
		if other == client {
			list = append(list[:i:i], list[i+1:]...)
			c.total--
			break
		}
	}
	if len(list) == 0 {
		delete(c.users, client.userID)
	} else {
		c.users[client.userID] = list
	}
}

func (c *SSEController) wakeUser(userID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, client := range c.users[userID] {
		client.poke()
	}
}

func (c *SSEController) wakeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, list := range c.users {
		for _, client := range list {
			client.poke()
		}
	}
}

// GetTotalConnections returns the number of open streams on this replica.
func (c *SSEController) GetTotalConnections() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func (c *SSEController) pollInterval() time.Duration {
	if c.wakeups != nil && c.wakeups.Healthy() {
		return c.pollHealthy
	}
	return c.pollUnhealthy
}

// Connect streams mailbox changes.
// GET /api/v1/sse/connect?token=<ticket>&cursor=<n|now>
func (c *SSEController) Connect(r *ghttp.Request) {
	claims := middleware.GetClaims(r)
	if claims == nil {
		response.Unauthorized(r, "Unauthorized")
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && cursor != "now" {
		if n, err := strconv.ParseInt(cursor, 10, 64); err != nil || n < 0 {
			response.BadRequest(r, "cursor must be a non-negative integer or now")
			return
		}
	}
	client, ok := c.register(claims.UserID)
	if !ok {
		response.WithStatus(r, 503, 503, "Too many live connections; try again later", nil)
		return
	}
	defer c.unregister(client)

	ctx := r.Context()
	s := &sseStream{feed: c.feed, userID: claims.UserID, ctx: ctx, w: sseResponseWriter{r}}
	if cursor == "" || cursor == "now" {
		now, err := c.feed.Changes(ctx, claims.UserID, "now", 0)
		if err != nil {
			response.InternalError(r, "Unable to open the live update stream")
			return
		}
		cursor = now.NextCursor
	}
	s.cursor = cursor

	r.Response.Header().Set("Content-Type", "text/event-stream")
	r.Response.Header().Set("Cache-Control", "no-cache")
	r.Response.Header().Set("Connection", "keep-alive")
	r.Response.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering
	if s.emit("", "connected", map[string]any{"clientId": client.id, "cursor": cursor}) != nil {
		return
	}
	c.serve(ctx, client, s)
}

// serve runs the wake loop until the client goes away, is evicted, or its
// credential ends. The first sync replays anything after the given cursor.
func (c *SSEController) serve(ctx context.Context, client *sseClient, s *sseStream) {
	heartbeat := time.NewTicker(c.heartbeat)
	defer heartbeat.Stop()
	credential := time.NewTicker(c.credentialCheck)
	defer credential.Stop()
	poll := time.NewTimer(0)
	defer poll.Stop()
	var counts <-chan time.Time
	var countsTimer *time.Timer
	defer func() {
		if countsTimer != nil {
			countsTimer.Stop()
		}
	}()
	syncNow := func() error {
		changed, err := s.sync()
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, errSSEWrite) {
				g.Log().Warningf(ctx, "Live update sync failed for user %d: %v", s.userID, err)
			}
			return err
		}
		if changed && counts == nil {
			countsTimer = time.NewTimer(c.countsDebounce)
			counts = countsTimer.C
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-client.evicted:
			return
		case <-client.wake:
			if errors.Is(syncNow(), errSSEWrite) {
				return
			}
		case <-poll.C:
			if errors.Is(syncNow(), errSSEWrite) {
				return
			}
			poll.Reset(c.pollInterval())
		case <-counts:
			counts = nil
			if s.emitCounts() != nil {
				return
			}
		case <-heartbeat.C:
			if s.emit("", "heartbeat", map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339)}) != nil {
				return
			}
		case <-credential.C:
			if !middleware.CredentialActive(ctx) {
				return
			}
		}
	}
}

var errSSEWrite = errors.New("live update stream closed")

type sseWriter interface {
	Write(p []byte) (int, error)
	Flush()
}

type sseResponseWriter struct{ r *ghttp.Request }

func (w sseResponseWriter) Write(p []byte) (int, error) {
	if err := w.r.Context().Err(); err != nil {
		return 0, err
	}
	w.r.Response.Write(p)
	return len(p), nil
}
func (w sseResponseWriter) Flush() { w.r.Response.Flush() }

// writeSSE writes one event in wire format; id is omitted when empty.
func writeSSE(w io.Writer, id, event string, data any) error {
	body, err := json.Marshal(SSEEvent{Type: event, Data: data})
	if err != nil {
		return err
	}
	frame := ""
	if id != "" {
		frame = "id: " + id + "\n"
	}
	frame += "event: " + event + "\ndata: " + string(body) + "\n\n"
	_, err = io.WriteString(w, frame)
	return err
}

// sseStream is one client's position in its mailbox feed.
type sseStream struct {
	feed   mailboxFeed
	userID int64
	ctx    context.Context
	w      sseWriter
	cursor string
}

func (s *sseStream) emit(id, event string, data any) error {
	if err := writeSSE(s.w, id, event, data); err != nil {
		return fmt.Errorf("%w: %v", errSSEWrite, err)
	}
	s.w.Flush()
	return nil
}

func (s *sseStream) emitCounts() error {
	counts, err := s.feed.GetReceivedEmailCounts(s.ctx, s.userID, 0)
	if err != nil {
		return nil // the next change retries
	}
	return s.emit("", "counts_update", map[string]any{"counts": counts})
}

// resync moves the client to the current cursor; it then reloads everything.
func (s *sseStream) resync(reason string) error {
	now, err := s.feed.Changes(s.ctx, s.userID, "now", 0)
	if err != nil {
		return err
	}
	s.cursor = now.NextCursor
	return s.emit("", "resync", map[string]any{"cursor": s.cursor, "reason": reason})
}

// sync emits every change after the stream's cursor and reports whether any
// was sent.
func (s *sseStream) sync() (bool, error) {
	changed := false
	for {
		res, err := s.feed.Changes(s.ctx, s.userID, s.cursor, sseBatch)
		switch {
		case errors.Is(err, service.ErrMailboxCursorExpired):
			return changed, s.resync(sseResyncExpired)
		case errors.Is(err, service.ErrMailboxCursorAhead):
			return changed, s.resync(sseResyncAhead)
		case err != nil:
			return changed, err
		}
		if res.HasMore {
			now, err := s.feed.Changes(s.ctx, s.userID, "now", 0)
			if err != nil {
				return changed, err
			}
			if cursorGap(s.cursor, now.NextCursor) > sseResyncPending {
				return changed, s.resync(sseResyncTooMany)
			}
		}
		if len(res.Changes) == 0 {
			return changed, nil
		}
		if err = s.emitBatch(res.Changes); err != nil {
			return changed, err
		}
		changed = true
		s.cursor = res.NextCursor
		if !res.HasMore {
			return changed, nil
		}
	}
}

func cursorGap(from, to string) int64 {
	a, _ := strconv.ParseInt(from, 10, 64)
	b, _ := strconv.ParseInt(to, 10, 64)
	return b - a
}

// coalescedChange is a message's net change within one batch.
type coalescedChange struct {
	last    service.MailboxChange
	created bool
	cursor  int64
}

// coalesceChanges keeps one entry per message, ordered by its last change, so
// the final event of a batch carries the batch's cursor. A message created in
// the batch is still new to the client even if it was updated afterwards.
func coalesceChanges(changes []service.MailboxChange) []coalescedChange {
	byUUID := map[string]*coalescedChange{}
	for _, ch := range changes {
		n, _ := strconv.ParseInt(ch.Cursor, 10, 64)
		entry := byUUID[ch.MessageUUID]
		if entry == nil {
			entry = &coalescedChange{}
			byUUID[ch.MessageUUID] = entry
		}
		entry.last, entry.cursor = ch, n
		if ch.Operation == "created" {
			entry.created = true
		}
	}
	out := make([]coalescedChange, 0, len(byUUID))
	for _, entry := range byUUID {
		out = append(out, *entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].cursor < out[j].cursor })
	return out
}

func (s *sseStream) emitBatch(changes []service.MailboxChange) error {
	merged := coalesceChanges(changes)
	var live []string
	for _, ch := range merged {
		if ch.last.Operation != "deleted" {
			live = append(live, ch.last.MessageUUID)
		}
	}
	summaries, err := s.feed.MailboxSummaries(s.ctx, s.userID, live)
	if err != nil {
		return err
	}
	for _, ch := range merged {
		id, uuid := ch.last.Cursor, ch.last.MessageUUID
		summary, ok := summaries[uuid]
		switch {
		case ch.last.Operation == "deleted" || !ok:
			err = s.emit(id, "email_deleted", map[string]any{"cursor": id, "uuids": []string{uuid}})
		case ch.created:
			err = s.emit(id, "new_email", map[string]any{"cursor": id, "uuid": uuid, "identityId": ch.last.IdentityID, "summary": summary})
		default:
			err = s.emit(id, "email_update", map[string]any{"cursor": id, "uuid": uuid, "summary": summary})
		}
		if err != nil {
			return err
		}
	}
	return nil
}
