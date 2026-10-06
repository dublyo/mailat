package service

import (
	"context"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lib/pq"
)

// MailboxChannel is notified by the mailbox_changes trigger with the owner's
// user id at commit, so every API replica learns about every mailbox change.
const MailboxChannel = "mailat_mailbox"

const mailboxNotifierPing = 30 * time.Second

// MailboxNotifier holds one LISTEN connection per process and fans wakeups out
// to the live-update hub. Notifications are only hints: the hub always reads
// the durable mailbox_changes feed, so a lost NOTIFY delays an event but never
// drops it.
type MailboxNotifier struct {
	dsn     string
	healthy atomic.Bool
	mu      sync.RWMutex
	onUser  func(int64)
	onAll   func()
}

func NewMailboxNotifier(databaseURL string) *MailboxNotifier {
	return &MailboxNotifier{dsn: databaseURL}
}

// SetHandlers registers the wakeup callbacks: onUser for one user's change,
// onAll after a reconnect or failure, when notifications may have been missed.
func (n *MailboxNotifier) SetHandlers(onUser func(int64), onAll func()) {
	n.mu.Lock()
	n.onUser, n.onAll = onUser, onAll
	n.mu.Unlock()
}

// Healthy reports whether the LISTEN connection is up; while it is not, the
// hub polls more often.
func (n *MailboxNotifier) Healthy() bool { return n != nil && n.healthy.Load() }

func (n *MailboxNotifier) user(id int64) {
	n.mu.RLock()
	fn := n.onUser
	n.mu.RUnlock()
	if fn != nil {
		fn(id)
	}
}

func (n *MailboxNotifier) all() {
	n.mu.RLock()
	fn := n.onAll
	n.mu.RUnlock()
	if fn != nil {
		fn()
	}
}

func (n *MailboxNotifier) event(ev pq.ListenerEventType, err error) {
	switch ev {
	case pq.ListenerEventConnected, pq.ListenerEventReconnected:
		n.healthy.Store(true)
	case pq.ListenerEventDisconnected, pq.ListenerEventConnectionAttemptFailed:
		if n.healthy.Swap(false) && err != nil {
			log.Printf("Mailbox listener disconnected: %v", err)
		}
	}
}

// Run listens until ctx ends. It returns only after the connection is closed.
func (n *MailboxNotifier) Run(ctx context.Context) {
	l := pq.NewListener(n.dsn, 10*time.Second, time.Minute, n.event)
	var closer sync.WaitGroup
	done := make(chan struct{})
	closer.Add(1)
	go func() {
		defer closer.Done()
		select {
		case <-ctx.Done():
		case <-done:
		}
		// Close also unblocks a Listen that is still waiting for a connection.
		_ = l.Close()
	}()
	defer func() {
		close(done)
		closer.Wait()
		// pq closes Notify once its loop has exited; draining keeps that loop
		// from blocking on a full buffer. A reconnect sleep may outlast the wait.
		drained := make(chan struct{})
		go func() {
			for range l.Notify {
			}
			close(drained)
		}()
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
		}
	}()

	if err := l.Listen(MailboxChannel); err != nil {
		if ctx.Err() == nil {
			log.Printf("Mailbox listener cannot LISTEN: %v", err)
		}
		n.healthy.Store(false)
		return
	}
	n.healthy.Store(true)
	// Anything committed before LISTEN took effect is picked up by a full wake.
	n.all()
	ping := time.NewTicker(mailboxNotifierPing)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			n.healthy.Store(false)
			return
		case note, ok := <-l.Notify:
			if !ok {
				n.healthy.Store(false)
				return
			}
			if note == nil {
				// pq sends nil after re-establishing the connection.
				n.all()
				continue
			}
			if id, err := strconv.ParseInt(note.Extra, 10, 64); err == nil && id > 0 {
				n.user(id)
			}
		case <-ping.C:
			if err := l.Ping(); err != nil {
				n.healthy.Store(false)
				n.all()
			}
		}
	}
}
