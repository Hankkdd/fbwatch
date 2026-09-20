// Package notify 把新 listing 送到外部管道。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Discord struct {
	WebhookURL string
	Client     *http.Client
}

func NewDiscord(webhookURL string) *Discord {
	return &Discord{
		WebhookURL: webhookURL,
		Client:     &http.Client{Timeout: 15 * time.Second},
	}
}

func (d *Discord) Enabled() bool { return d.WebhookURL != "" }

// Send 送出一則訊息。目前只傳貼文連結。
func (d *Discord) Send(ctx context.Context, content string) error {
	body, err := json.Marshal(map[string]string{"content": content})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := d.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Discord webhook 有速率限制，429 要退讓重試而不是當成失敗
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("discord 速率限制 (429)")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord 回應 %d", resp.StatusCode)
	}
	return nil
}
