package notify

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRouterEmptyNameUsesDefault(t *testing.T) {
	def := NewDiscord("https://discord.com/api/webhooks/default")
	r := NewRouter(def, env(nil))
	dc, err := r.For("")
	if err != nil {
		t.Fatal(err)
	}
	if dc != def {
		t.Error("沒指定時應該用預設管道")
	}
}

func TestRouterResolvesNamedWebhook(t *testing.T) {
	def := NewDiscord("https://discord.com/api/webhooks/default")
	r := NewRouter(def, env(map[string]string{"HOOK_B": "https://discord.com/api/webhooks/b"}))
	dc, err := r.For("HOOK_B")
	if err != nil {
		t.Fatal(err)
	}
	if dc.WebhookURL != "https://discord.com/api/webhooks/b" {
		t.Errorf("實得 %q", dc.WebhookURL)
	}
	if dc == def {
		t.Error("指定的管道不該是預設那個")
	}
}

// 這是整個路由最重要的一條：指定了管道但找不到時，絕對不能退回預設。
// 分流的理由就是「不要送到那裡」，送錯聊天室收不回來，少送一則下一輪還會再試。
func TestRouterNeverFallsBackToDefaultWhenEnvMissing(t *testing.T) {
	def := NewDiscord("https://discord.com/api/webhooks/default")
	r := NewRouter(def, env(nil))
	dc, err := r.For("HOOK_MISSING")
	if err == nil {
		t.Fatal("環境變數沒設定時應該回報錯誤")
	}
	if dc != nil {
		t.Errorf("不該回傳任何管道，實得 %v", dc.WebhookURL)
	}
	if !strings.Contains(err.Error(), "HOOK_MISSING") {
		t.Errorf("錯誤訊息應該點出是哪個變數: %v", err)
	}
}

// 空字串跟沒設定一樣要擋：.env 裡留一行 FOO= 是很常見的手滑
func TestRouterTreatsEmptyEnvAsMissing(t *testing.T) {
	r := NewRouter(NewDiscord("https://discord.com/api/webhooks/default"),
		env(map[string]string{"HOOK_B": ""}))
	if _, err := r.For("HOOK_B"); err == nil {
		t.Error("空字串應該跟沒設定一樣被擋下")
	}
}

// 同一個名稱重複問要拿到同一個 client，不要每則貼文都開一個新的 http.Client
func TestRouterCachesPerName(t *testing.T) {
	r := NewRouter(NewDiscord("https://discord.com/api/webhooks/default"),
		env(map[string]string{"HOOK_B": "https://discord.com/api/webhooks/b"}))
	a, _ := r.For("HOOK_B")
	b, _ := r.For("HOOK_B")
	if a != b {
		t.Error("同一個名稱應該回同一個 client")
	}
}

func TestRouterDefaultIsReachable(t *testing.T) {
	def := NewDiscord("https://discord.com/api/webhooks/default")
	if NewRouter(def, env(nil)).Default() != def {
		t.Error("Default 應該回傳建構時那個管道")
	}
}

// 告警是故障時最後一個還在動的東西，不該被一個 nil 打死。
// 看門狗就是這樣：它在整支程式卡住時才會跑，那時候 panic 等於沒有防線。
func TestNilDiscordIsNotEnabledInsteadOfPanicking(t *testing.T) {
	var dc *Discord
	if dc.Enabled() {
		t.Error("nil 不該回報為可用")
	}
}
