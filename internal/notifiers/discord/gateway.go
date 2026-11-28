package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"nhooyr.io/websocket"
)

type gwPresence struct {
	Since      int64  `json:"since"`
	Activities []any  `json:"activities"`
	Status     string `json:"status"`
	AFK        bool   `json:"afk"`
}

type gwPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d,omitempty"`
}

type gwIdentify struct {
	Token      string            `json:"token"`
	Intents    int               `json:"intents"`
	Properties map[string]string `json:"properties"`
	Presence   gwPresence        `json:"presence"`
}

func startGateway(ctx context.Context, token string, intents int, onInteraction func(ctx context.Context, data json.RawMessage)) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			if err := runGateway(ctx, token, intents, onInteraction); err != nil {
				log.Printf("[discord] gateway: %v (reconnect in 5s)", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
}

func runGateway(ctx context.Context, token string, intents int, onInteraction func(ctx context.Context, data json.RawMessage)) error {
	c, _, err := websocket.Dial(ctx, "wss://gateway.discord.gg/?v=10&encoding=json", nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	c.SetReadLimit(1 << 20)

	_, msg, err := c.Read(ctx)
	if err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	var hello struct {
		Op int `json:"op"`
		D  struct {
			HeartbeatInterval int `json:"heartbeat_interval"`
		} `json:"d"`
	}
	if err := json.Unmarshal(msg, &hello); err != nil {
		return fmt.Errorf("parse hello: %w", err)
	}
	if hello.Op != 10 {
		return fmt.Errorf("expected op 10, got %d", hello.Op)
	}

	interval := time.Duration(hello.D.HeartbeatInterval) * time.Millisecond
	hctx, hcancel := context.WithCancel(ctx)
	defer hcancel()

	var mu sync.Mutex
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-hctx.Done():
				return
			case <-t.C:
			}
			mu.Lock()
			err := c.Write(hctx, websocket.MessageText, []byte(`{"op":1,"d":null}`))
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	identBytes, _ := json.Marshal(gwPayload{
		Op: 2,
		D: mustJSON(gwIdentify{
			Token:   token,
			Intents: intents,
			Properties: map[string]string{
				"os":      "linux",
				"browser": "mailer",
				"device":  "mailer",
			},
			Presence: gwPresence{
				Since:      0,
				Activities: []any{},
				Status:     "online",
				AFK:        false,
			},
		}),
	})

	mu.Lock()
	err = c.Write(ctx, websocket.MessageText, identBytes)
	mu.Unlock()
	if err != nil {
		return fmt.Errorf("identify: %w", err)
	}

	for {
		_, msg, err := c.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		var p gwPayload
		if err := json.Unmarshal(msg, &p); err != nil {
			continue
		}
		switch p.Op {
		case 0:
			var ev struct {
				Type string          `json:"t"`
				Data json.RawMessage `json:"d"`
			}
			if err := json.Unmarshal(msg, &ev); err != nil {
				continue
			}
			switch ev.Type {
			case "READY":
				log.Printf("[discord] gateway: connected and online")
			case "INTERACTION_CREATE":
				if onInteraction != nil {
					onInteraction(ctx, ev.Data)
				}
			}
		case 1:
			mu.Lock()
			c.Write(ctx, websocket.MessageText, []byte(`{"op":1,"d":null}`))
			mu.Unlock()
		case 7:
			return fmt.Errorf("server requested reconnect")
		case 9:
			return fmt.Errorf("invalid session")
		}
	}
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return json.RawMessage(b)
}
