package plugins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 通知适配器：Bark / Telegram / ntfy / 通用 webhook，各自只发一次 HTTP 请求。
// 事件 Payload 约定键：title、body、cli、host、project、session；适配器只读取这些键。

const (
	payloadTitle = "title"
	payloadBody  = "body"
)

func newNotifierClient() *http.Client { return &http.Client{Timeout: 5 * time.Second} }

func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("无法编码通知请求")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("通知服务返回 %s", resp.Status)
	}
	return nil
}

func payloadField(ev NotificationEvent, key string) string {
	value, _ := ev.Payload[key].(string)
	return value
}

type WebhookNotifier struct {
	URL    string
	Bearer string
	client *http.Client
}

func NewWebhook(url, bearer string) (Notifier, error) {
	if url == "" {
		return nil, fmt.Errorf("webhook 通道需要 URL")
	}
	return &WebhookNotifier{URL: url, Bearer: bearer, client: newNotifierClient()}, nil
}

func (n *WebhookNotifier) Notify(ctx context.Context, ev NotificationEvent) error {
	body := map[string]any{"type": ev.Type}
	for key, value := range ev.Payload {
		body[key] = value
	}
	headers := map[string]string{}
	if n.Bearer != "" {
		headers["Authorization"] = "Bearer " + n.Bearer
	}
	return postJSON(ctx, n.client, n.URL, headers, body)
}

type BarkNotifier struct {
	Server string
	Key    string
	client *http.Client
}

func NewBark(server, key string) (Notifier, error) {
	if key == "" {
		return nil, fmt.Errorf("Bark 通道需要设备 key")
	}
	if server == "" {
		server = "https://api.day.app"
	}
	return &BarkNotifier{Server: strings.TrimRight(server, "/"), Key: key, client: newNotifierClient()}, nil
}

func (n *BarkNotifier) Notify(ctx context.Context, ev NotificationEvent) error {
	// Bark 约定 title 与 body 分开传递，不能拼成同一字段。
	return postJSON(ctx, n.client, n.Server+"/push", nil, map[string]any{
		"device_key": n.Key,
		"title":      payloadField(ev, payloadTitle),
		"body":       payloadField(ev, payloadBody),
		"group":      "relay",
	})
}

type TelegramNotifier struct {
	Server string
	Token  string
	ChatID string
	client *http.Client
}

func NewTelegram(server, token, chatID string) (Notifier, error) {
	if token == "" || chatID == "" {
		return nil, fmt.Errorf("Telegram 通道需要 bot token 与 chat id")
	}
	if server == "" {
		server = "https://api.telegram.org"
	}
	return &TelegramNotifier{Server: strings.TrimRight(server, "/"), Token: token, ChatID: chatID, client: newNotifierClient()}, nil
}

func (n *TelegramNotifier) Notify(ctx context.Context, ev NotificationEvent) error {
	text := payloadField(ev, payloadTitle) + "\n\n" + payloadField(ev, payloadBody)
	return postJSON(ctx, n.client, n.Server+"/bot"+n.Token+"/sendMessage", nil, map[string]any{
		"chat_id": n.ChatID,
		"text":    text,
	})
}

type NtfyNotifier struct {
	Server string
	Topic  string
	Token  string
	client *http.Client
}

func NewNtfy(server, topic, token string) (Notifier, error) {
	if topic == "" {
		return nil, fmt.Errorf("ntfy 通道需要 topic")
	}
	if server == "" {
		server = "https://ntfy.sh"
	}
	return &NtfyNotifier{Server: strings.TrimRight(server, "/"), Topic: topic, Token: token, client: newNotifierClient()}, nil
}

func (n *NtfyNotifier) Notify(ctx context.Context, ev NotificationEvent) error {
	headers := map[string]string{}
	if n.Token != "" {
		headers["Authorization"] = "Bearer " + n.Token
	}
	return postJSON(ctx, n.client, n.Server, headers, map[string]any{
		"topic":   n.Topic,
		"title":   payloadField(ev, payloadTitle),
		"message": payloadField(ev, payloadBody),
		"tags":    []string{"robot"},
	})
}
