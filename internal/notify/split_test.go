package notify

import (
	"strings"
	"testing"
)

func TestSplitKeepsShortMessageIntact(t *testing.T) {
	got := Split("一張三百，要的請密我", 100)
	if len(got) != 1 || got[0] != "一張三百，要的請密我" {
		t.Fatalf("短訊息不該被動到: %q", got)
	}
}

func TestSplitEmpty(t *testing.T) {
	if got := Split("   \n  ", 100); got != nil {
		t.Fatalf("空白內容不該產生訊息: %q", got)
	}
}

// 不改字是硬性要求：切出來的段落接回去必須與原文完全相同
// （除了切點上的換行）。被截掉的往往正是後半段的品項與價格。
func TestSplitLosesNoContent(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("第")
		b.WriteString(strings.Repeat("項", 30))
		b.WriteString("\n\n")
	}
	orig := b.String()

	parts := Split(orig, 200)
	if len(parts) < 2 {
		t.Fatalf("長文應被切成多段，實得 %d 段", len(parts))
	}

	strip := func(s string) string { return strings.ReplaceAll(s, "\n", "") }
	if strip(strings.Join(parts, "")) != strip(orig) {
		t.Fatal("切段後內容與原文不符 —— 不改字是硬性要求")
	}
}

func TestSplitRespectsLimit(t *testing.T) {
	long := strings.Repeat("戰鎚模型出售", 500)
	for _, p := range Split(long, 200) {
		if n := len([]rune(p)); n > 200 {
			t.Fatalf("段落長度 %d 超過上限 200", n)
		}
	}
}

// 優先在段落或換行處切，不要切在句子中間
func TestSplitPrefersLineBreaks(t *testing.T) {
	text := strings.Repeat("這是一行內容還算長一點\n", 40)
	parts := Split(text, 120)
	if len(parts) < 2 {
		t.Fatal("應被切成多段")
	}
	for i, p := range parts[:len(parts)-1] {
		if strings.HasSuffix(p, "長一點") || strings.HasSuffix(p, "\n") {
			continue
		}
		t.Errorf("第 %d 段結尾切在句中: %q", i+1, lastRunes(p, 12))
	}
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
