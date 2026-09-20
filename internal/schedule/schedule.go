// Package schedule 決定下一次輪詢的間隔。
//
// 刻意不用 cron：固定間隔本身就是機器人簽章。
package schedule

import (
	"math/rand"
	"time"
)

type Config struct {
	DayMin, DayMax     time.Duration
	NightMin, NightMax time.Duration
	NightStart         int // 幾點進入夜間（含）
	NightEnd           int // 幾點離開夜間（不含）
}

func Default() Config {
	return Config{
		DayMin: 90 * time.Second, DayMax: 240 * time.Second,
		NightMin: 600 * time.Second, NightMax: 1200 * time.Second,
		NightStart: 1, NightEnd: 6,
	}
}

// Next 回傳下次輪詢前要等多久。
//
// 夜間拉長是因為每兩分鐘重整到天亮不是人類行為。
func (c Config) Next(now time.Time) time.Duration {
	min, max := c.DayMin, c.DayMax
	if c.isNight(now.Hour()) {
		min, max = c.NightMin, c.NightMax
	}
	if max <= min {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(max-min)))
}

func (c Config) isNight(hour int) bool {
	if c.NightStart < c.NightEnd {
		return hour >= c.NightStart && hour < c.NightEnd
	}
	// 跨午夜
	return hour >= c.NightStart || hour < c.NightEnd
}
