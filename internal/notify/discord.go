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

// Enabled 回報這個管道能不能送。nil 也要安全 ——
// 呼叫端大多是告警路徑，而告警是故障時最後一個還在動的東西，
// 它不該因為通知管道沒設好就 panic。
func (d *Discord) Enabled() bool { return d != nil && d.WebhookURL != "" }

// Embed 是 Discord 的卡片訊息，只留我們用得到的欄位。
type Embed struct {
	Title       string       `json:"title,omitempty"`
	URL         string       `json:"url,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Author      *EmbedAuthor `json:"author,omitempty"`
	Image       *EmbedImage  `json:"image,omitempty"`
	Thumbnail   *EmbedImage  `json:"thumbnail,omitempty"`
}

type EmbedAuthor struct {
	Name string `json:"name"`
}

type EmbedImage struct {
	URL string `json:"url"`
}

// Send 送出一則純文字訊息。FB 貼文走這條，內文原封不動。
func (d *Discord) Send(ctx context.Context, content string) error {
	return d.SendMentioning(ctx, content, "")
}

// SendMentioning 和 Send 一樣，但允許 content 裡的 <@userID> 真的通知到那個人。
// 用來tag 比價 bot 出來看這則貼文；userID 為空就誰都不通知。
func (d *Discord) SendMentioning(ctx context.Context, content, userID string) error {
	return d.post(ctx, map[string]any{"content": content, "allowed_mentions": allowedMentions(userID)})
}

// SendEmbed 送出一則帶卡片的訊息。
//
// content 仍然要給：手機推播顯示的是 content，只有 embed 的訊息
// 推播出來會是空的一條，等於白做。卡片是給點開來看的，
// 推播那一行是給在通知列上判斷要不要點開的。
func (d *Discord) SendEmbed(ctx context.Context, content string, e Embed) error {
	return d.SendEmbedMentioning(ctx, content, e, "")
}

// SendEmbedMentioning 是 SendEmbed 加上 SendMentioning 的通知規則。
func (d *Discord) SendEmbedMentioning(ctx context.Context, content string, e Embed, userID string) error {
	return d.post(ctx, map[string]any{
		"content": content, "embeds": []Embed{e}, "allowed_mentions": allowedMentions(userID),
	})
}

// allowedMentions 明確列出誰可以被通知。
//
// 不給的話 Discord 會照 content 解析：貼文內文裡寫了 @everyone、@here 或
// <@某人>，webhook 就會真的通知全伺服器或那個人。貼文是任何人都能寫的，
// 所以預設一個都不通知，只放行我們自己加上去的那一個。
func allowedMentions(userID string) map[string]any {
	am := map[string]any{"parse": []string{}}
	if userID != "" {
		am["users"] = []string{userID}
	}
	return am
}

func (d *Discord) post(ctx context.Context, payload map[string]any) error {
	body, err := json.Marshal(payload)
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
