package notify

import (
	"strings"
	"testing"
)

func TestMutedMessageIncludesNameAndLink(t *testing.T) {
	const link = "https://www.facebook.com/commerce/listing/123/"
	got := MutedMessage("RAY Studio", link)

	if !strings.Contains(got, "RAY Studio") {
		t.Errorf("訊息應含賣家名稱: %q", got)
	}
	// 靜音不等於藏起來，連結仍要附上
	if !strings.Contains(got, "<"+link+">") {
		t.Errorf("連結應以 <> 包住以抑制預覽卡: %q", got)
	}
}

func TestMutedMessageFallsBackWithoutName(t *testing.T) {
	got := MutedMessage("  ", "https://example.com/x")
	if strings.Contains(got, "%s") {
		t.Errorf("模板未被替換: %q", got)
	}
	if !strings.Contains(got, "某賣家") {
		t.Errorf("沒有名稱時應有替代稱呼: %q", got)
	}
}

// 一行的重點是短。太長就失去「只佔一行」的意義。
func TestMutedMessageStaysShort(t *testing.T) {
	got := MutedMessage("某個名字很長的賣家帳號名稱", "https://www.facebook.com/commerce/listing/1234567890123456/")
	if n := len([]rune(got)); n > 120 {
		t.Errorf("訊息長度 %d 字，應維持在一行以內: %q", n, got)
	}
}

func TestMutedMessageVaries(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 60; i++ {
		seen[MutedMessage("RAY", "https://example.com/x")] = true
	}
	if len(seen) < 2 {
		t.Error("每次都同一句會很無聊，應該有變化")
	}
}
