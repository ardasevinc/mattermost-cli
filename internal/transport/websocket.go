package transport

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
)

type MessageType int

const (
	MessageText   MessageType = MessageType(websocket.MessageText)
	MessageBinary MessageType = MessageType(websocket.MessageBinary)
)

type WebSocket interface {
	Read(context.Context) (MessageType, []byte, error)
	Write(context.Context, MessageType, []byte) error
	Close(context.Context) error
	CloseNow() error
	SetReadLimit(int64)
}

type socketAdapter struct{ conn *websocket.Conn }

func (socketAdapter socketAdapter) Read(ctx context.Context) (MessageType, []byte, error) {
	type_, data, err := socketAdapter.conn.Read(ctx)
	return MessageType(type_), data, err
}
func (socketAdapter socketAdapter) Write(ctx context.Context, type_ MessageType, data []byte) error {
	return socketAdapter.conn.Write(ctx, websocket.MessageType(type_), data)
}
func (socketAdapter socketAdapter) Close(ctx context.Context) error {
	done := make(chan error, 1)
	go func() { done <- socketAdapter.conn.Close(websocket.StatusNormalClosure, "") }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = socketAdapter.conn.CloseNow()
		<-done
		return ctx.Err()
	}
}
func (socketAdapter socketAdapter) CloseNow() error          { return socketAdapter.conn.CloseNow() }
func (socketAdapter socketAdapter) SetReadLimit(limit int64) { socketAdapter.conn.SetReadLimit(limit) }

type DialWebSocket func(context.Context, string) (WebSocket, error)

func Dial(ctx context.Context, target string) (WebSocket, error) {
	conn, response, err := websocket.Dial(ctx, target, &websocket.DialOptions{HTTPClient: http.DefaultClient})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return socketAdapter{conn: conn}, nil
}
