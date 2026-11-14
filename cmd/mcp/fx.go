package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// 匯率來源。台灣銀行的牌告頁從這台機器抓會跳機器人驗證，改用 ExchangeRate-API 的公開端點：
// 免金鑰、每天更新一次，回傳的是市場中價（銀行賣你外幣的價會再高一點，差不到 1%）。
const fxURL = "https://open.er-api.com/v6/latest/USD"

// fxTTL 是快取時間。來源一天才更新一次，比價一天也才幾次，沒必要每次都打。
const fxTTL = 6 * time.Hour

type fxCache struct {
	mu      sync.Mutex
	rates   map[string]float64 // 1 USD = rates[X] X
	updated string             // 來源標示的更新時間
	fetched time.Time
	client  *http.Client
}

func newFXCache() *fxCache {
	return &fxCache{client: &http.Client{Timeout: 15 * time.Second}}
}

func (c *fxCache) get(ctx context.Context) (map[string]float64, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rates != nil && time.Since(c.fetched) < fxTTL {
		return c.rates, c.updated, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fxURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	var body struct {
		Result  string             `json:"result"`
		Updated string             `json:"time_last_update_utc"`
		Rates   map[string]float64 `json:"rates"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, "", err
	}
	if body.Result != "success" || body.Rates["TWD"] == 0 {
		return nil, "", fmt.Errorf("匯率來源回傳異常（result=%q）", body.Result)
	}
	c.rates, c.updated, c.fetched = body.Rates, body.Updated, time.Now()
	return c.rates, c.updated, nil
}

type fxIn struct {
	Currency string  `json:"currency,omitempty" jsonschema:"外幣代碼，預設 USD；也可以是 GBP、EUR、JPY、AUD 等"`
	Amount   float64 `json:"amount,omitempty" jsonschema:"要換算成台幣的外幣金額，可省略"`
}

// toTWD 回傳 1 單位外幣等於多少台幣。來源以美金為基準，其他幣別用交叉匯率算。
func toTWD(rates map[string]float64, cur string) (float64, bool) {
	twd, base := rates["TWD"], rates[cur]
	if twd == 0 || base == 0 {
		return 0, false
	}
	return twd / base, true
}

func (t *tools) fx(ctx context.Context, _ *mcp.CallToolRequest, in fxIn) (*mcp.CallToolResult, any, error) {
	cur := strings.ToUpper(strings.TrimSpace(in.Currency))
	if cur == "" {
		cur = "USD"
	}
	rates, updated, err := t.fxc.get(ctx)
	if err != nil {
		return textResult("暫時查不到匯率：" + err.Error()), nil, nil
	}
	r, ok := toTWD(rates, cur)
	if !ok {
		return textResult(fmt.Sprintf("沒有 %s 的匯率。", cur)), nil, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "1 %s ≈ %.2f TWD（市場中價，來源 ExchangeRate-API，更新於 %s）\n", cur, r, updated)
	if in.Amount > 0 {
		fmt.Fprintf(&b, "%.2f %s ≈ NT$%s\n", in.Amount, cur, comma(int(in.Amount*r+0.5)))
	}
	return textResult(b.String()), nil, nil
}
