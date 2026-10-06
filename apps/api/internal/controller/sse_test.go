package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/dublyo/mailat/api/internal/model"
	"github.com/dublyo/mailat/api/internal/service"
)

// fakeFeed serves a fixed change log for user 1.
type fakeFeed struct {
	changes   []service.MailboxChange
	current   int64
	retained  int64
	summaries map[string]model.ReceivedEmail
}

func (f *fakeFeed) Changes(_ context.Context, _ int64, cursor string, limit int) (*service.MailboxChanges, error) {
	if cursor == "now" {
		return &service.MailboxChanges{NextCursor: strconv.FormatInt(f.current, 10)}, nil
	}
	after, _ := strconv.ParseInt(cursor, 10, 64)
	if after < f.retained {
		return nil, service.ErrMailboxCursorExpired
	}
	if after > f.current {
		return nil, service.ErrMailboxCursorAhead
	}
	out := &service.MailboxChanges{NextCursor: cursor}
	for _, c := range f.changes {
		n, _ := strconv.ParseInt(c.Cursor, 10, 64)
		if n <= after {
			continue
		}
		if len(out.Changes) == limit {
			out.HasMore = true
			break
		}
		out.Changes = append(out.Changes, c)
		out.NextCursor = c.Cursor
	}
	return out, nil
}

func (f *fakeFeed) MailboxSummaries(_ context.Context, _ int64, uuids []string) (map[string]model.ReceivedEmail, error) {
	out := map[string]model.ReceivedEmail{}
	for _, id := range uuids {
		if s, ok := f.summaries[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

func (f *fakeFeed) GetReceivedEmailCounts(context.Context, int64, int64) (*model.InboxCountsResponse, error) {
	return &model.InboxCountsResponse{Inbox: 1}, nil
}

func (f *fakeFeed) add(uuid, op string) {
	f.current++
	f.changes = append(f.changes, service.MailboxChange{Cursor: strconv.FormatInt(f.current, 10), MessageUUID: uuid, IdentityID: 3, Operation: op})
}

type bufferWriter struct{ bytes.Buffer }

func (*bufferWriter) Flush() {}

type wireEvent struct {
	ID, Event string
	Data      map[string]any
}

func parseWire(t *testing.T, raw string) []wireEvent {
	t.Helper()
	var out []wireEvent
	for _, frame := range strings.Split(strings.TrimSuffix(raw, "\n\n"), "\n\n") {
		var e wireEvent
		for _, line := range strings.Split(frame, "\n") {
			key, value, _ := strings.Cut(line, ": ")
			switch key {
			case "id":
				e.ID = value
			case "event":
				e.Event = value
			case "data":
				var body SSEEvent
				if err := json.Unmarshal([]byte(value), &body); err != nil {
					t.Fatalf("bad data line %q: %v", line, err)
				}
				if body.Type != e.Event {
					t.Fatalf("type %q does not match event %q", body.Type, e.Event)
				}
				e.Data, _ = body.Data.(map[string]any)
			default:
				t.Fatalf("unexpected line %q", line)
			}
		}
		out = append(out, e)
	}
	return out
}

func newTestStream(f *fakeFeed, cursor string) (*sseStream, *bufferWriter) {
	w := &bufferWriter{}
	return &sseStream{feed: f, userID: 1, ctx: context.Background(), w: w, cursor: cursor}, w
}

func TestSSECoalescesByMessageAndCarriesCursorIDs(t *testing.T) {
	f := &fakeFeed{summaries: map[string]model.ReceivedEmail{"a": {UUID: "a", Folder: "archive"}, "b": {UUID: "b", Folder: "inbox"}, "c": {UUID: "c"}}}
	f.add("a", "created")
	f.add("b", "updated")
	f.add("a", "updated") // still new to the client, now in archive
	f.add("c", "updated")
	f.add("c", "deleted")
	f.add("d", "updated") // copy already gone: reported as deleted
	s, w := newTestStream(f, "0")
	changed, err := s.sync()
	if err != nil || !changed || s.cursor != "6" {
		t.Fatalf("changed=%v cursor=%s err=%v", changed, s.cursor, err)
	}
	events := parseWire(t, w.String())
	want := []struct{ id, event, uuid string }{{"2", "email_update", "b"}, {"3", "new_email", "a"}, {"5", "email_deleted", "c"}, {"6", "email_deleted", "d"}}
	if len(events) != len(want) {
		t.Fatalf("got %d events: %s", len(events), w.String())
	}
	for i, e := range events {
		if e.ID != want[i].id || e.Event != want[i].event || e.Data["cursor"] != want[i].id {
			t.Fatalf("event %d = %+v, want %+v", i, e, want[i])
		}
		if e.Event == "email_deleted" {
			if uuids, _ := e.Data["uuids"].([]any); len(uuids) != 1 || uuids[0] != want[i].uuid {
				t.Fatalf("deleted uuids %v", e.Data["uuids"])
			}
			continue
		}
		summary, _ := e.Data["summary"].(map[string]any)
		if e.Data["uuid"] != want[i].uuid || summary["uuid"] != want[i].uuid {
			t.Fatalf("event %d carries the wrong message: %+v", i, e.Data)
		}
	}
	if folder := events[1].Data["summary"].(map[string]any)["folder"]; folder != "archive" || events[1].Data["identityId"] != float64(3) {
		t.Fatalf("new_email must carry the final row: %+v", events[1].Data)
	}
	w.Reset()
	if changed, err = s.sync(); err != nil || changed || w.Len() != 0 {
		t.Fatalf("idle sync emitted %q (changed=%v err=%v)", w.String(), changed, err)
	}
}

func TestSSEResyncsOnBacklogExpiryAndRestore(t *testing.T) {
	f := &fakeFeed{summaries: map[string]model.ReceivedEmail{}}
	for i := 0; i < sseResyncPending+1; i++ {
		f.add("x"+strconv.Itoa(i), "created")
	}
	s, w := newTestStream(f, "0")
	if _, err := s.sync(); err != nil {
		t.Fatal(err)
	}
	events := parseWire(t, w.String())
	if len(events) != 1 || events[0].Event != "resync" || events[0].Data["reason"] != sseResyncTooMany || s.cursor != "501" || events[0].Data["cursor"] != "501" {
		t.Fatalf("backlog above %d must resync: %+v", sseResyncPending, events)
	}

	// Exactly the replay limit is still replayed (in batches).
	f = &fakeFeed{summaries: map[string]model.ReceivedEmail{}}
	for i := 0; i < sseResyncPending; i++ {
		f.add("y"+strconv.Itoa(i), "deleted")
	}
	s, w = newTestStream(f, "0")
	if _, err := s.sync(); err != nil || s.cursor != "500" || len(parseWire(t, w.String())) != sseResyncPending {
		t.Fatalf("replay of %d changes failed: cursor=%s err=%v", sseResyncPending, s.cursor, err)
	}

	f = &fakeFeed{current: 9, retained: 5}
	for cursor, reason := range map[string]string{"2": sseResyncExpired, "12": sseResyncAhead} {
		s, w = newTestStream(f, cursor)
		if _, err := s.sync(); err != nil {
			t.Fatal(err)
		}
		events = parseWire(t, w.String())
		if len(events) != 1 || events[0].Event != "resync" || events[0].Data["reason"] != reason || s.cursor != "9" {
			t.Fatalf("cursor %s: %+v", cursor, events)
		}
	}
}

func TestSSEUserCapEvictsOldestAndGlobalCapRefuses(t *testing.T) {
	c := NewSSEController(nil, &fakeFeed{}, sseUserCap+1)
	var clients []*sseClient
	for i := 0; i <= sseUserCap; i++ {
		client, ok := c.register(1)
		if !ok {
			t.Fatal("register refused under the cap")
		}
		clients = append(clients, client)
	}
	select {
	case <-clients[0].evicted:
	default:
		t.Fatal("oldest stream was not closed at the per-user cap")
	}
	for _, client := range clients[1:] {
		select {
		case <-client.evicted:
			t.Fatal("a newer stream was closed")
		default:
		}
	}
	if c.GetTotalConnections() != sseUserCap {
		t.Fatalf("total %d", c.GetTotalConnections())
	}
	other, ok := c.register(2)
	if !ok {
		t.Fatal("second user refused")
	}
	if _, ok = c.register(3); ok {
		t.Fatal("replica accepted more than the global cap")
	}
	// Wakeups reach only the user's own streams.
	c.wakeUser(2)
	select {
	case <-other.wake:
	default:
		t.Fatal("user 2 not woken")
	}
	for _, client := range clients[1:] {
		select {
		case <-client.wake:
			t.Fatal("wake leaked across accounts")
		default:
		}
	}
	c.wakeAll()
	if len(clients[1].wake) != 1 || len(other.wake) != 1 {
		t.Fatal("wakeAll missed a stream")
	}
	c.unregister(clients[0]) // already evicted: no double count
	for _, client := range clients[1:] {
		c.unregister(client)
	}
	c.unregister(other)
	if c.GetTotalConnections() != 0 || len(c.users) != 0 {
		t.Fatalf("registry not empty: %d", c.GetTotalConnections())
	}
}

func TestWriteSSEFormat(t *testing.T) {
	var b bytes.Buffer
	if err := writeSSE(&b, "42", "email_deleted", map[string]any{"cursor": "42", "uuids": []string{"u"}}); err != nil {
		t.Fatal(err)
	}
	if want := "id: 42\nevent: email_deleted\ndata: {\"type\":\"email_deleted\",\"data\":{\"cursor\":\"42\",\"uuids\":[\"u\"]}}\n\n"; b.String() != want {
		t.Fatalf("wire format %q", b.String())
	}
	b.Reset()
	_ = writeSSE(&b, "", "heartbeat", map[string]any{})
	if strings.Contains(b.String(), "id:") {
		t.Fatal("non-change events must not move the client's cursor")
	}
}
