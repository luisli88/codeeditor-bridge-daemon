package ws

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Handler processes one Envelope for a given Channel. Implementations live
// in the internal packages that own that channel (gitmanager, entitlements,
// bootstrap, debug, session, ...) and are wired in via Server.Handle.
type Handler func(ctx context.Context, conn *Conn, env Envelope)

// Server multiplexes every channel from contracts/websocket-protocol.md over
// a single WebSocket connection per client session.
type Server struct {
	mu       sync.RWMutex
	handlers map[Channel]Handler
}

// NewServer returns a Server with no channels registered yet.
func NewServer() *Server {
	return &Server{handlers: make(map[Channel]Handler)}
}

// Handle registers the Handler responsible for a Channel. Registering the
// same Channel twice replaces the previous Handler.
func (s *Server) Handle(channel Channel, h Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[channel] = h
}

func (s *Server) handlerFor(channel Channel) (Handler, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	h, ok := s.handlers[channel]
	return h, ok
}

// ServeHTTP upgrades the connection and dispatches incoming Envelopes to the
// Handler registered for their Channel. Envelopes on the same Channel are
// processed strictly in the order they arrive (FIFO); different Channels run
// concurrently so a slow LSP response never blocks git status updates.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		log.Printf("ws: accept failed: %v", err)
		return
	}
	conn := &Conn{ws: c}
	defer c.CloseNow() //nolint:errcheck

	ctx := r.Context()
	queues := make(map[Channel]chan Envelope)
	var wg sync.WaitGroup

	queueFor := func(channel Channel) chan Envelope {
		if q, ok := queues[channel]; ok {
			return q
		}
		q := make(chan Envelope, 32)
		queues[channel] = q
		handler, ok := s.handlerFor(channel)
		wg.Add(1)
		go func() {
			defer wg.Done()
			for env := range q {
				if !ok {
					conn.SendError(env, "unknown-channel", "no handler registered for this channel", nil)
					continue
				}
				handler(ctx, conn, env)
			}
		}()
		return q
	}

	for {
		var env Envelope
		if err := wsjson.Read(ctx, c, &env); err != nil {
			break
		}
		queueFor(env.Channel) <- env
	}

	for _, q := range queues {
		close(q)
	}
	wg.Wait()
}

// Conn is the per-connection handle Handlers use to write Envelopes back to
// the client, in the same order they call Send.
type Conn struct {
	writeMu sync.Mutex
	ws      *websocket.Conn
}

// Send writes an Envelope to the client.
func (c *Conn) Send(ctx context.Context, env Envelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.ws, env)
}

// SendPayload marshals payload and sends it as the Payload of an Envelope
// correlated to the given request id, on the given channel.
func (c *Conn) SendPayload(ctx context.Context, id string, channel Channel, workspaceID *string, payload any) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.Send(ctx, Envelope{ID: id, Channel: channel, WorkspaceID: workspaceID, Payload: raw})
}

// SendError responds to req with a structured ErrorPayload, per the
// "Errores" section of contracts/websocket-protocol.md.
func (c *Conn) SendError(req Envelope, code, message string, requirementRef *string) {
	_ = c.Send(context.Background(), Envelope{
		ID:          req.ID,
		Channel:     req.Channel,
		WorkspaceID: req.WorkspaceID,
		Error:       &ErrorPayload{Code: code, Message: message, RequirementRef: requirementRef},
	})
}
