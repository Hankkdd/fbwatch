// Package breaker 決定連續失敗後要繼續、冷卻還是停止。
//
// 存在的理由只有一個：**重試迴圈會把軟性封鎖升級成硬封鎖**。
// FB 對可疑行為先給軟性阻擋，這時停手通常能恢復；持續重試才是把帳號
// 推向永久封鎖的原因。所以這裡的預設一律偏向「停下來等人」。
package breaker

type State int

const (
	Normal  State = iota
	Cooling       // 暫停一段時間後再試一次
	Tripped       // 停止，等人工處理
)

type Breaker struct {
	// MaxFailures 是進入冷卻前容許的連續失敗次數。
	MaxFailures int

	consecutive int
	cooled      bool // 冷卻後的那次重試機會已經用掉
	state       State
	reason      string
}

func New(maxFailures int) *Breaker {
	return &Breaker{MaxFailures: maxFailures}
}

func (b *Breaker) State() State   { return b.state }
func (b *Breaker) Reason() string { return b.reason }
func (b *Breaker) Tripped() bool  { return b.state == Tripped }

// Success 清除失敗計數，並歸還冷卻後的重試機會。
func (b *Breaker) Success() {
	b.consecutive = 0
	b.cooled = false
	if b.state == Cooling {
		b.state = Normal
	}
}

// Fatal 立即熔斷，不冷卻也不重試。
//
// 用於登入態失效或帳號被 checkpoint —— 這兩種情況重試只會讓事情更糟，
// 而且需要人工介入才能恢復，機器再試幾次都沒有意義。
func (b *Breaker) Fatal(reason string) {
	b.state = Tripped
	b.reason = reason
}

// Fail 記錄一次可重試的失敗，回傳記錄後的狀態。
//
// 連續失敗達 MaxFailures 進入冷卻；冷卻後的重試再失敗就熔斷。
func (b *Breaker) Fail(reason string) State {
	if b.state == Tripped {
		return b.state
	}
	b.consecutive++

	if b.cooled {
		b.state = Tripped
		b.reason = "冷卻後重試仍失敗：" + reason
		return b.state
	}
	if b.consecutive >= b.MaxFailures {
		b.cooled = true
		b.state = Cooling
		b.reason = reason
		return b.state
	}
	return Normal
}
