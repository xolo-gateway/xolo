package mcpclient

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/pkg/errors"
)

// DefaultIdleTimeout is how long a Connector keeps its session open without
// being used, when it is left unset. It is deliberately generous: a session is
// what makes an MCP server's own per-conversation state survive the tool rounds
// of a request, and a model may think for a minute between two calls.
const DefaultIdleTimeout = 5 * time.Minute

// Connector opens MCP sessions with fixed connection settings and keeps its
// session alive between calls, so a request resolving several tool calls does
// not open a session per call.
//
// An MCP session is more than a connection: the Streamable HTTP transport pins
// the exchange to a session ID, and a server keeping per-session state (a
// database handle, a query plan, a paginated result set) loses it when a new one
// starts. A gateway opening a session per tool call therefore pays the
// initialization on every round and breaks any tool whose result refers to what
// a previous one left behind.
//
// Concurrent callers share the session; the MCP client session is itself
// concurrency safe. A session the server ended is detected by Do, which replaces
// it and retries once, rather than handing a dead session out.
type Connector struct {
	cfg         Config
	idleTimeout time.Duration
	now         func() time.Time

	mu       sync.Mutex
	session  *mcp.ClientSession
	inFlight int
	lastUsed time.Time
	closed   bool
	stop     chan struct{}
	// connectMu serializes the opens: two callers finding no session at the
	// same instant would otherwise each open one, defeating the very reuse the
	// connector exists for. It is held only while no session exists, so calls
	// on a live session never wait on it.
	connectMu sync.Mutex
}

// NewConnector returns a Connector opening sessions with cfg. idleTimeout <= 0
// falls back to DefaultIdleTimeout.
func NewConnector(cfg Config, idleTimeout time.Duration) *Connector {
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	return &Connector{cfg: cfg, idleTimeout: idleTimeout, now: time.Now}
}

// SetConfig updates the settings the next session is opened with, and closes the
// current one when the credentials changed.
//
// A connector outlives the credentials it was created with: an OAuth access
// token expires, an administrator rotates a static secret. A session opened on
// the former keeps sending it on every exchange, since the transport pins the
// session ID and the headers with it, so a stale credential would fail every
// call of the conversation, retry included. Replacing the session is what makes
// the next call use the new ones.
func (c *Connector) SetConfig(cfg Config) {
	c.mu.Lock()
	changed := cfg.AuthValue != c.cfg.AuthValue ||
		cfg.AuthHeaderName != c.cfg.AuthHeaderName ||
		cfg.Endpoint != c.cfg.Endpoint
	c.cfg = cfg
	if changed {
		c.discardLocked()
	}
	c.mu.Unlock()
}

// Do runs fn on the connector's session, opening one when there is none.
//
// A call failing because the session is over (the server restarted, a proxy
// dropped the connection) is retried once on a fresh session: a session kept
// between requests is exactly the thing that outlives the server it was opened
// on, and one retry is what tells a stale session from an operation that fails
// on its own.
func (c *Connector) Do(ctx context.Context, fn func(*mcp.ClientSession) error) error {
	session, err := c.acquire(ctx)
	if err != nil {
		return err
	}

	err = fn(session)
	if err == nil || !isSessionOver(err) {
		c.done()
		return err
	}

	// The session is over: drop it (a concurrent caller may already have) and
	// try once on a new one.
	c.done()
	c.Reset()

	retry, retryErr := c.acquire(ctx)
	if retryErr != nil {
		return retryErr
	}
	defer c.done()
	return fn(retry)
}

// isSessionOver reports whether err is the session having ended rather than the
// operation having failed.
func isSessionOver(err error) bool {
	return errors.Is(err, mcp.ErrConnectionClosed)
}

// acquire returns a session for one call, opening it when needed. Every
// successful acquire must be matched by a done.
func (c *Connector) acquire(ctx context.Context) (*mcp.ClientSession, error) {
	c.mu.Lock()
	if c.session != nil && !usable(c.session) {
		c.discardLocked()
	}
	if c.session != nil {
		c.inFlight++
		c.lastUsed = c.now()
		session := c.session
		c.mu.Unlock()
		return session, nil
	}
	c.mu.Unlock()

	// Only one caller opens a session at a time: the others wait here and then
	// find the one it opened. Holding c.mu itself through the handshake would
	// serialize every call, including those on a live session.
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	// Someone may have opened a session while we waited.
	c.mu.Lock()
	if c.session != nil {
		c.inFlight++
		c.lastUsed = c.now()
		session := c.session
		c.mu.Unlock()
		return session, nil
	}
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return nil, errors.New("mcpclient: connector closed")
	}
	cfg := c.cfg

	session, err := Connect(ctx, cfg)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = session.Close()
		return nil, errors.New("mcpclient: connector closed")
	}
	if c.session != nil {
		// Unreachable while connectMu is held: kept so a future change to the
		// locking cannot silently leak the session just opened.
		c.inFlight++
		c.lastUsed = c.now()
		existing := c.session
		c.mu.Unlock()
		_ = session.Close()
		return existing, nil
	}
	if c.session != nil {
		// Another caller's session is over: ours replaces it.
		c.discardLocked()
	}
	c.session = session
	c.inFlight = 1
	c.lastUsed = c.now()
	c.stop = make(chan struct{})
	stop := c.stop
	c.mu.Unlock()

	go c.expire(stop)

	return session, nil
}

// done ends one call on the session. It is what starts the idle countdown.
func (c *Connector) done() {
	c.mu.Lock()
	if c.inFlight > 0 {
		c.inFlight--
	}
	c.lastUsed = c.now()
	c.mu.Unlock()
}

// Reset closes the current session so the next call opens a new one: a plugin
// calls it when the credentials behind the settings changed.
func (c *Connector) Reset() {
	c.mu.Lock()
	c.discardLocked()
	c.mu.Unlock()
}

// Close closes the current session and stops the Connector. The Connector stays
// usable: a later Do opens a new session.
func (c *Connector) Close() {
	c.mu.Lock()
	c.closed = true
	c.discardLocked()
	c.mu.Unlock()
}

// usable reports whether session can serve a call now.
//
// Liveness is not probed here: the MCP client session offers no non-blocking
// check (Wait blocks until the session ends). A dead session is caught by Do,
// which retries once, and a session that never finished its handshake is caught
// by its missing initialize result.
func usable(session *mcp.ClientSession) bool {
	return session.InitializeResult() != nil
}

// discardLocked closes the current session, if any. The caller holds c.mu.
func (c *Connector) discardLocked() {
	session, stop := c.session, c.stop
	c.session, c.stop, c.inFlight = nil, nil, 0
	if session == nil {
		return
	}
	if stop != nil {
		close(stop)
	}
	// Closing waits for nothing: a caller gone during its tool round must not
	// keep the session open. The MCP session fails its in-flight calls with
	// ErrConnectionClosed, which Do turns into one retry.
	go func() { _ = session.Close() }()
}

// expire closes the session once it has been idle for idleTimeout.
func (c *Connector) expire(stop chan struct{}) {
	ticker := time.NewTicker(c.idleTimeout / 4)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.stop != stop {
				// Replaced meanwhile: this goroutine is the stale one.
				c.mu.Unlock()
				return
			}
			if c.inFlight == 0 && c.now().Sub(c.lastUsed) >= c.idleTimeout {
				c.discardLocked()
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}
}
