// Package tests holds integration/contract tests that exercise more than
// one internal package together — see tasks.md T017.
package tests

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/require"

	"github.com/luisli88/codeeditor-bridge-daemon/internal/ws"
)

// TestEnvelope_RoundTrip validates the envelope shape from
// specs/001-core-development-flows/contracts/websocket-protocol.md: a
// registered channel handler receives the Envelope and can reply,
// correlated by id.
func TestEnvelope_RoundTrip(t *testing.T) {
	srv := ws.NewServer()
	srv.Handle(ws.ChannelEntitlements, func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		require.NoError(t, conn.SendPayload(ctx, env.ID, env.Channel, env.WorkspaceID, map[string]any{
			"allowed": true,
		}))
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{
		ID:      "req-1",
		Channel: ws.ChannelEntitlements,
	}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	require.Equal(t, "req-1", resp.ID)
	require.Equal(t, ws.ChannelEntitlements, resp.Channel)
	require.Nil(t, resp.Error)
}

// TestEnvelope_PerChannelFIFO validates that messages on the same channel
// are handled in the order they arrive, per "NUNCA reordena mensajes entre
// canales de un mismo workspaceId" in the contract.
func TestEnvelope_PerChannelFIFO(t *testing.T) {
	srv := ws.NewServer()
	var mu sync.Mutex
	var order []string
	srv.Handle(ws.ChannelGit, func(ctx context.Context, conn *ws.Conn, env ws.Envelope) {
		mu.Lock()
		order = append(order, env.ID)
		mu.Unlock()
		_ = conn.SendPayload(ctx, env.ID, env.Channel, env.WorkspaceID, map[string]any{"ack": true})
	})

	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	ids := []string{"a", "b", "c", "d", "e"}
	for _, id := range ids {
		require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: id, Channel: ws.ChannelGit}))
	}
	for range ids {
		var resp ws.Envelope
		require.NoError(t, wsjson.Read(ctx, c, &resp))
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, ids, order)
}

// TestEnvelope_UnknownChannel validates the structured error response for a
// channel with no registered handler.
func TestEnvelope_UnknownChannel(t *testing.T) {
	srv := ws.NewServer()
	ts := httptest.NewServer(srv)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(ts.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer c.CloseNow() //nolint:errcheck

	require.NoError(t, wsjson.Write(ctx, c, ws.Envelope{ID: "req-x", Channel: ws.ChannelDebug}))

	var resp ws.Envelope
	require.NoError(t, wsjson.Read(ctx, c, &resp))
	require.NotNil(t, resp.Error)
	require.Equal(t, "unknown-channel", resp.Error.Code)
}
