package gateway

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/internal/tokenhelper"
	"github.com/disgoorg/disgo/json"
	"github.com/disgoorg/log"

	"github.com/gorilla/websocket"
)

var _ Gateway = (*gatewayImpl)(nil)

// New creates a new Gateway instance with the provided token, eventHandlerFunc, closeHandlerFunc and ConfigOpt(s).
func New(token string, eventHandlerFunc EventHandlerFunc, closeHandlerFunc CloseHandlerFunc, opts ...ConfigOpt) Gateway {
	config := DefaultConfig()
	config.Apply(opts)

	return &gatewayImpl{
		config:           *config,
		eventHandlerFunc: eventHandlerFunc,
		closeHandlerFunc: closeHandlerFunc,
		token:            token,
		status:           StatusUnconnected,
	}
}

type gatewayImpl struct {
	config           Config
	eventHandlerFunc EventHandlerFunc
	closeHandlerFunc CloseHandlerFunc
	token            string

	conn            *websocket.Conn
	connMu          sync.Mutex
	heartbeatTicker *time.Ticker
	status          Status

	heartbeatInterval     time.Duration
	lastHeartbeatSent     time.Time
	lastHeartbeatReceived time.Time
}

func (g *gatewayImpl) Logger() log.Logger {
	return g.config.Logger
}

func (g *gatewayImpl) ShardID() int {
	return g.config.ShardID
}

func (g *gatewayImpl) ShardCount() int {
	return g.config.ShardCount
}

func (g *gatewayImpl) SessionID() *string {
	return g.config.SessionID
}

func (g *gatewayImpl) LastSequenceReceived() *int {
	return g.config.LastSequenceReceived
}

func (g *gatewayImpl) GatewayIntents() discord.GatewayIntents {
	return g.config.GatewayIntents
}

func (g *gatewayImpl) formatLogsf(format string, a ...any) string {
	if g.config.ShardCount > 1 {
		return fmt.Sprintf("[%d/%d] %s", g.config.ShardID, g.config.ShardCount, fmt.Sprintf(format, a...))
	}
	return fmt.Sprintf(format, a...)
}

func (g *gatewayImpl) formatLogs(a ...any) string {
	if g.config.ShardCount > 1 {
		return fmt.Sprintf("[%d/%d] %s", g.config.ShardID, g.config.ShardCount, fmt.Sprint(a...))
	}
	return fmt.Sprint(a...)
}

func (g *gatewayImpl) Open(ctx context.Context) error {
	g.Logger().Debug(g.formatLogs("opening gateway connection"))

	g.connMu.Lock()
	defer g.connMu.Unlock()
	if g.conn != nil {
		return discord.ErrGatewayAlreadyConnected
	}
	g.status = StatusConnecting

	gatewayURL := fmt.Sprintf("%s?v=%d&encoding=json", g.config.GatewayURL, Version)
	g.lastHeartbeatSent = time.Now().UTC()
	conn, rs, err := g.config.Dialer.DialContext(ctx, gatewayURL, nil)
	if err != nil {
		g.Close(ctx)
		body := "null"
		if rs != nil && rs.Body != nil {
			defer func() {
				_ = rs.Body.Close()
			}()
			rawBody, bErr := io.ReadAll(rs.Body)
			if bErr != nil {
				g.Logger().Error(g.formatLogs("error while reading response body: ", err))
			}
			body = string(rawBody)
		}

		g.Logger().Error(g.formatLogsf("error connecting to the gateway. url: %s, error: %s, body: %s", gatewayURL, err, body))
		return err
	}

	conn.SetCloseHandler(func(code int, text string) error {
		return nil
	})

	g.conn = conn

	// reset rate limiter when connecting
	g.config.RateLimiter.Reset()

	g.status = StatusWaitingForHello

	go g.listen(conn)

	return nil
}

func (g *gatewayImpl) Close(ctx context.Context) {
	g.CloseWithCode(ctx, websocket.CloseNormalClosure, "Shutting down")
}

func (g *gatewayImpl) CloseWithCode(ctx context.Context, code int, message string) {
	if g.heartbeatTicker != nil {
		g.Logger().Debug(g.formatLogs("closing heartbeat goroutines..."))
		g.heartbeatTicker.Stop()
		g.heartbeatTicker = nil
	}

	g.connMu.Lock()
	defer g.connMu.Unlock()
	if g.conn != nil {
		g.config.RateLimiter.Close(ctx)
		g.Logger().Debug(g.formatLogsf("closing gateway connection with code: %d, message: %s", code, message))
		if err := g.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, message)); err != nil && err != websocket.ErrCloseSent {
			g.Logger().Debug(g.formatLogs("error writing close code. error: ", err))
		}
		_ = g.conn.Close()
		g.conn = nil

		// clear resume data as we closed gracefully
		if code == websocket.CloseNormalClosure || code == websocket.CloseGoingAway {
			g.config.SessionID = nil
			g.config.LastSequenceReceived = nil
		}
	}

}

func (g *gatewayImpl) Status() Status {
	g.connMu.Lock()
	defer g.connMu.Unlock()
	return g.status
}

func (g *gatewayImpl) Send(ctx context.Context, op discord.GatewayOpcode, d discord.GatewayMessageData) error {
	data, err := json.Marshal(discord.GatewayMessage{
		Op: op,
		D:  d,
	})
	if err != nil {
		return err
	}
	return g.send(ctx, websocket.TextMessage, data)
}

func (g *gatewayImpl) send(ctx context.Context, messageType int, data []byte) error {
	g.connMu.Lock()
	defer g.connMu.Unlock()
	if g.conn == nil {
		return discord.ErrShardNotConnected
	}

	if err := g.config.RateLimiter.Wait(ctx); err != nil {
		return err
	}

	defer g.config.RateLimiter.Unlock()
	g.Logger().Trace(g.formatLogs("sending gateway command: ", string(data)))
	return g.conn.WriteMessage(messageType, data)
}

func (g *gatewayImpl) Latency() time.Duration {
	return g.lastHeartbeatReceived.Sub(g.lastHeartbeatSent)
}

func (g *gatewayImpl) reconnectTry(ctx context.Context, try int, delay time.Duration) error {
	if try >= g.config.MaxReconnectTries-1 {
		return fmt.Errorf("failed to reconnect. exceeded max reconnect tries of %d reached", g.config.MaxReconnectTries)
	}
	timer := time.NewTimer(time.Duration(try) * delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		timer.Stop()
		return ctx.Err()
	case <-timer.C:
	}

	g.Logger().Debug(g.formatLogs("reconnecting gateway..."))
	if err := g.Open(ctx); err != nil {
		if err == discord.ErrGatewayAlreadyConnected {
			return err
		}
		g.Logger().Error(g.formatLogs("failed to reconnect gateway. error: ", err))
		g.status = StatusDisconnected
		return g.reconnectTry(ctx, try+1, delay)
	}
	return nil
}

func (g *gatewayImpl) reconnect(ctx context.Context) {
	err := g.reconnectTry(ctx, 0, time.Second)
	if err != nil {
		g.Logger().Error(g.formatLogs("failed to reopen gateway", err))
	}
}

func (g *gatewayImpl) heartbeat() {
	g.heartbeatTicker = time.NewTicker(g.heartbeatInterval)
	defer g.heartbeatTicker.Stop()
	defer g.Logger().Debug(g.formatLogs("exiting heartbeat goroutine..."))

	for range g.heartbeatTicker.C {
		g.sendHeartbeat()
	}
}

func (g *gatewayImpl) sendHeartbeat() {
	g.Logger().Debug(g.formatLogs("sending heartbeat..."))

	ctx, cancel := context.WithTimeout(context.Background(), g.heartbeatInterval)
	defer cancel()
	if err := g.Send(ctx, discord.GatewayOpcodeHeartbeat, (*discord.GatewayMessageDataHeartbeat)(g.config.LastSequenceReceived)); err != nil && err != discord.ErrShardNotConnected {
		g.Logger().Error(g.formatLogs("failed to send heartbeat. error: ", err))
		g.CloseWithCode(context.TODO(), websocket.CloseServiceRestart, "heartbeat timeout")
		go g.reconnect(context.TODO())
		return
	}
	g.lastHeartbeatSent = time.Now().UTC()
}

func (g *gatewayImpl) identify() {
	g.status = StatusIdentifying
	g.Logger().Debug(g.formatLogs("sending Identify command..."))

	identify := discord.GatewayMessageDataIdentify{
		Token: g.token,
		Properties: discord.IdentifyCommandDataProperties{
			OS:      g.config.OS,
			Browser: g.config.Browser,
			Device:  g.config.Device,
		},
		Compress:       g.config.Compress,
		LargeThreshold: g.config.LargeThreshold,
		GatewayIntents: g.config.GatewayIntents,
		Presence:       g.config.Presence,
	}
	if g.ShardCount() > 1 {
		identify.Shard = &[2]int{g.ShardID(), g.ShardCount()}
	}

	if err := g.Send(context.TODO(), discord.GatewayOpcodeIdentify, identify); err != nil {
		g.Logger().Error(g.formatLogs("error sending Identify command err: ", err))
	}
	g.status = StatusWaitingForReady
}

func (g *gatewayImpl) resume() {
	g.status = StatusResuming
	resume := discord.GatewayMessageDataResume{
		Token:     g.token,
		SessionID: *g.config.SessionID,
		Seq:       *g.config.LastSequenceReceived,
	}

	g.Logger().Debug(g.formatLogs("sending Resume command..."))
	if err := g.Send(context.TODO(), discord.GatewayOpcodeResume, resume); err != nil {
		g.Logger().Error(g.formatLogs("error sending resume command err: ", err))
	}
}

func (g *gatewayImpl) listen(conn *websocket.Conn) {
	defer g.Logger().Debug(g.formatLogs("exiting listen goroutine..."))
loop:
	for {
		mt, reader, err := conn.NextReader()
		if err != nil {
			g.connMu.Lock()
			sameConnection := g.conn == conn
			g.connMu.Unlock()

			// if sameConnection is false, it means the connection has been closed by the user, and we can just exit
			if !sameConnection {
				return
			}

			reconnect := true
			if closeError, ok := err.(*websocket.CloseError); ok {
				closeCode := discord.GatewayCloseEventCode(closeError.Code)
				reconnect = closeCode.ShouldReconnect()

				if closeCode == discord.GatewayCloseEventCodeDisallowedIntents {
					var intentsURL string
					if id, err := tokenhelper.IDFromToken(g.token); err == nil {
						intentsURL = fmt.Sprintf("https://discord.com/developers/applications/%s/bot", *id)
					} else {
						intentsURL = "https://discord.com/developers/applications"
					}
					g.Logger().Error(g.formatLogsf("disallowed gateway intents supplied. go to %s and enable the privileged intent for your application. intents: %d", intentsURL, g.config.GatewayIntents))
				} else if closeCode == discord.GatewayCloseEventCodeInvalidSeq {
					g.Logger().Error(g.formatLogs("invalid sequence provided. reconnecting..."))
					g.config.LastSequenceReceived = nil
					g.config.SessionID = nil
				} else {
					g.Logger().Error(g.formatLogsf("gateway close received, reconnect: %t, code: %d, error: %s", g.config.AutoReconnect && reconnect, closeError.Code, closeError.Text))
				}
			} else if errors.Is(err, net.ErrClosed) {
				// we closed the connection ourselves. Don't try to reconnect here
				reconnect = false
			} else {
				g.Logger().Debug(g.formatLogs("failed to read next message from gateway. error: ", err))
			}

			if g.config.AutoReconnect && reconnect {
				go g.reconnect(context.TODO())
			} else {
				g.Close(context.TODO())
				if g.closeHandlerFunc != nil {
					go g.closeHandlerFunc(g, err)
				}
			}
			break loop
		}

		event, err := g.parseGatewayMessage(mt, reader)
		if err != nil {
			g.Logger().Error(g.formatLogs("error while parsing gateway event. error: ", err))
			continue
		}

		switch event.Op {
		case discord.GatewayOpcodeHello:
			g.lastHeartbeatReceived = time.Now().UTC()
			go g.heartbeat()

			g.heartbeatInterval = time.Duration(event.D.(discord.GatewayMessageDataHello).HeartbeatInterval) * time.Millisecond

			if g.config.LastSequenceReceived == nil || g.config.SessionID == nil {
				g.identify()
			} else {
				g.resume()
			}

		case discord.GatewayOpcodeDispatch:
			data := event.D.(discord.GatewayMessageDataDispatch)
			g.Logger().Trace(g.formatLogsf("received: OpcodeDispatch %s, data: %s", event.T, string(data)))

			// set last sequence received
			g.config.LastSequenceReceived = &event.S

			// get session id here
			if event.T == discord.GatewayEventTypeReady {
				var readyEvent discord.GatewayEventReady
				if err = json.Unmarshal(data, &readyEvent); err != nil {
					g.Logger().Error(g.formatLogs("Error parsing ready event. error: ", err))
					continue
				}
				g.config.SessionID = &readyEvent.SessionID
				g.status = StatusReady
				g.Logger().Debug(g.formatLogs("ready event received"))
			}

			// push event to the command manager
			g.eventHandlerFunc(event.T, event.S, g.config.ShardID, bytes.NewBuffer(data))

		case discord.GatewayOpcodeHeartbeat:
			g.Logger().Debug(g.formatLogs("received: OpcodeHeartbeat"))
			g.sendHeartbeat()

		case discord.GatewayOpcodeReconnect:
			g.Logger().Debug(g.formatLogs("received: OpcodeReconnect"))
			g.CloseWithCode(context.TODO(), websocket.CloseServiceRestart, "received reconnect")
			go g.reconnect(context.TODO())
			break loop

		case discord.GatewayOpcodeInvalidSession:
			canResume := event.D.(discord.GatewayMessageDataInvalidSession)
			g.Logger().Debug(g.formatLogs("received: OpcodeInvalidSession, canResume: ", canResume))

			code := websocket.CloseNormalClosure
			if canResume {
				code = websocket.CloseServiceRestart
			} else {
				// clear resume info
				g.config.SessionID = nil
				g.config.LastSequenceReceived = nil
			}

			g.CloseWithCode(context.TODO(), code, "invalid session")
			go g.reconnect(context.TODO())
			break loop

		case discord.GatewayOpcodeHeartbeatACK:
			g.Logger().Debug(g.formatLogs("received: OpcodeHeartbeatACK"))
			g.lastHeartbeatReceived = time.Now().UTC()
		}
	}
}

func (g *gatewayImpl) parseGatewayMessage(mt int, reader io.Reader) (discord.GatewayMessage, error) {
	var finalReadCloser io.ReadCloser
	if mt == websocket.BinaryMessage {
		g.Logger().Trace(g.formatLogs("binary message received. decompressing..."))
		readCloser, err := zlib.NewReader(reader)
		if err != nil {
			return discord.GatewayMessage{}, fmt.Errorf("failed to decompress zlib: %w", err)
		}
		finalReadCloser = readCloser
	} else {
		finalReadCloser = io.NopCloser(reader)
	}
	defer func() {
		_ = finalReadCloser.Close()
	}()

	var message discord.GatewayMessage
	if err := json.NewDecoder(finalReadCloser).Decode(&message); err != nil {
		g.Logger().Error(g.formatLogs("error decoding websocket message: ", err))
		return discord.GatewayMessage{}, err
	}
	return message, nil
}
