package browser

import (
	"errors"
	"fmt"
	"testing"
)

// 熔斷靠 errors.Is 辨識登入態失效。若包裝方式錯了（例如用 %v 而非 %w），
// 熔斷永遠不會觸發，而且是靜默的 —— 系統會一直重試到帳號被硬封鎖。
func TestSessionInvalidSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("%w：被導向 https://www.facebook.com/checkpoint/", ErrSessionInvalid)
	if !errors.Is(err, ErrSessionInvalid) {
		t.Fatal("包裝後應仍可用 errors.Is 辨識")
	}
}

func TestOtherErrorsAreNotSessionInvalid(t *testing.T) {
	err := fmt.Errorf("等待 feed 渲染逾時（60s）")
	if errors.Is(err, ErrSessionInvalid) {
		t.Fatal("一般逾時不該被當成登入態失效 —— 那會讓偶發問題直接熔斷")
	}
}

func TestGroupURLForcesChronological(t *testing.T) {
	got := GroupURL("150336012291472")
	want := "https://www.facebook.com/groups/150336012291472/?sorting_setting=CHRONOLOGICAL"
	if got != want {
		t.Errorf("GroupURL = %q, want %q（少了排序參數會落在演算法排序的商品分頁）", got, want)
	}
}
