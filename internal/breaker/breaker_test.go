package breaker

import "testing"

func TestTransientFailuresBelowThreshold(t *testing.T) {
	b := New(3)
	if got := b.Fail("逾時"); got != Normal {
		t.Errorf("第 1 次失敗 = %v, want Normal", got)
	}
	if got := b.Fail("逾時"); got != Normal {
		t.Errorf("第 2 次失敗 = %v, want Normal", got)
	}
	if b.Tripped() {
		t.Error("偶發失敗不該熔斷")
	}
}

func TestCoolsAtThreshold(t *testing.T) {
	b := New(3)
	b.Fail("逾時")
	b.Fail("逾時")
	if got := b.Fail("逾時"); got != Cooling {
		t.Fatalf("第 3 次失敗 = %v, want Cooling", got)
	}
	if b.Tripped() {
		t.Error("進入冷卻不等於熔斷 —— 還有一次重試機會")
	}
}

// 冷卻後的重試再失敗就熔斷。這是規格 §4.9 的行為：
// 暫停 30 分鐘後重試一次，再失敗則停止。
func TestTripsWhenRetryAfterCoolingFails(t *testing.T) {
	b := New(3)
	for i := 0; i < 3; i++ {
		b.Fail("逾時")
	}
	if got := b.Fail("又逾時"); got != Tripped {
		t.Fatalf("冷卻後重試失敗 = %v, want Tripped", got)
	}
	if b.Reason() == "" {
		t.Error("熔斷應記錄原因，否則告警說不清發生什麼事")
	}
}

func TestSuccessRestoresRetryBudget(t *testing.T) {
	b := New(3)
	for i := 0; i < 3; i++ {
		b.Fail("逾時")
	}
	b.Success() // 冷卻後重試成功

	if b.State() != Normal {
		t.Fatalf("成功後 = %v, want Normal", b.State())
	}
	// 重試機會應歸還：再次連續失敗要重新走完 冷卻 → 熔斷
	b.Fail("逾時")
	b.Fail("逾時")
	if got := b.Fail("逾時"); got != Cooling {
		t.Fatalf("成功後再失敗 = %v, want Cooling（不該直接熔斷）", got)
	}
}

// 登入態失效與 checkpoint 不重試：重試只會讓軟性封鎖變成硬封鎖，
// 而且需要人工重新登入，機器再試幾次都沒有意義。
func TestFatalTripsImmediately(t *testing.T) {
	b := New(3)
	b.Fatal("被導向 /checkpoint/")
	if !b.Tripped() {
		t.Fatal("Fatal 應立即熔斷，不經過冷卻")
	}
	if b.Reason() != "被導向 /checkpoint/" {
		t.Errorf("Reason = %q", b.Reason())
	}
}

func TestTrippedIsTerminal(t *testing.T) {
	b := New(3)
	b.Fatal("登入態失效")
	if got := b.Fail("逾時"); got != Tripped {
		t.Errorf("熔斷後仍應維持 Tripped, got %v", got)
	}
	if b.Reason() != "登入態失效" {
		t.Errorf("熔斷原因不該被後續失敗覆蓋: %q", b.Reason())
	}
}
