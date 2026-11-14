package seed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "seed.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// 沒有設定檔是正常狀態：全部在資料庫裡設定的人不該被擋在啟動階段。
func TestLoadMissingFileIsNotAnError(t *testing.T) {
	f, err := Load(filepath.Join(t.TempDir(), "不存在.yaml"))
	if err != nil {
		t.Fatalf("檔案不存在不該是錯誤：%v", err)
	}
	if !f.Empty() {
		t.Error("不存在的檔案應該得到空設定")
	}
}

func TestLoadEmptyFileIsNotAnError(t *testing.T) {
	if _, err := Load(write(t, "")); err != nil {
		t.Fatalf("空檔案不該是錯誤：%v", err)
	}
}

func TestLoadReadsEverySection(t *testing.T) {
	f, err := Load(write(t, `
groups:
  - id: "123"
    name: 測試社團
    webhook_env: DISCORD_WEBHOOK_URL_B
    require_patterns: ["限量", "預購"]
keywords:
  - label: 限量
    line: 限量品出現了
    priority: 10
    patterns: ["限量", "限定"]
    summon: true
shops:
  - id: GM0000000000000
    name: 測試賣場
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Groups) != 1 || f.Groups[0].WebhookEnv != "DISCORD_WEBHOOK_URL_B" {
		t.Errorf("社團解析錯誤：%+v", f.Groups)
	}
	if len(f.Groups[0].RequirePatterns) != 2 {
		t.Errorf("主題過濾詞解析錯誤：%+v", f.Groups[0].RequirePatterns)
	}
	if len(f.Keywords) != 1 || !f.Keywords[0].Summon {
		t.Errorf("關鍵字解析錯誤：%+v", f.Keywords)
	}
	if len(f.Shops) != 1 {
		t.Errorf("賣場解析錯誤：%+v", f.Shops)
	}
}

// 欄位名打錯要當場停下來。寫成 require_pattern 少一個 s 而設定被默默忽略，
// 症狀是「過濾沒生效」，離成因很遠。
func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(write(t, "groups:\n  - id: \"1\"\n    require_pattern: [\"限量\"]\n"))
	if err == nil {
		t.Fatal("打錯的欄位名應該報錯")
	}
	if !strings.Contains(err.Error(), "require_pattern") {
		t.Errorf("錯誤訊息要指出是哪個欄位，實得：%v", err)
	}
}

// 下面幾個都是「設定看起來有填、實際不會生效」的狀況，必須在啟動時就擋下來。
func TestLoadRejectsIncompleteEntries(t *testing.T) {
	cases := map[string]string{
		"社團缺 id":        "groups:\n  - name: 沒有 id 的社團\n",
		"關鍵字缺 label":    "keywords:\n  - line: 嗨\n    patterns: [\"限量\"]\n",
		"關鍵字沒有 pattern": "keywords:\n  - label: 限量\n    line: 嗨\n    patterns: []\n",
		"賣場缺 id":        "shops:\n  - name: 沒有 id 的賣場\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Load(write(t, body)); err == nil {
				t.Error("應該報錯")
			}
		})
	}
}
