// Package schedule 決定下一次輪詢的間隔。
//
// 刻意不用 cron：固定間隔本身就是機器人簽章。
package schedule

import (
	"math/rand"
	"strconv"
	"time"
)

type Config struct {
	DayMin, DayMax     time.Duration
	NightMin, NightMax time.Duration
	NightStart         int // 幾點進入夜間（含）
	NightEnd           int // 幾點離開夜間（不含）
}

// Default 的夜間視窗是 02:00–05:59。
//
// 起點原本是 1 點，實測發現設錯了：01 點有 15 則貼文，跟下午差不多熱鬧，
// 但因為落在夜間視窗第一格，中位延遲 12.7 分，隔壁 00 點只有 7.6 分 ——
// 多等的 5 分鐘純粹是節奏造成的。02–05 點四小時加起來 20 則，
// 21 天下來一天一則，那裡慢才是對的。
func Default() Config {
	return Config{
		DayMin: 90 * time.Second, DayMax: 240 * time.Second,
		NightMin: 600 * time.Second, NightMax: 1200 * time.Second,
		NightStart: 2, NightEnd: 6,
	}
}

// FromEnv 以環境變數覆寫預設值，讓節奏可以調整而不必改程式。
//
// 調快的收益有限：實測 FB 自己把貼文放進動態牆就要 9–16 分鐘，
// 輪詢相位差只佔其中 1–2 分鐘。頻率加倍主要是換取資料，不是換取速度。
func FromEnv(getenv func(string) string) Config {
	c := Default()
	c.DayMin = envSecs(getenv, "FBWATCH_POLL_DAY_MIN", c.DayMin)
	c.DayMax = envSecs(getenv, "FBWATCH_POLL_DAY_MAX", c.DayMax)
	c.NightMin = envSecs(getenv, "FBWATCH_POLL_NIGHT_MIN", c.NightMin)
	c.NightMax = envSecs(getenv, "FBWATCH_POLL_NIGHT_MAX", c.NightMax)
	c.NightStart = envHour(getenv, "FBWATCH_NIGHT_START", c.NightStart)
	c.NightEnd = envHour(getenv, "FBWATCH_NIGHT_END", c.NightEnd)

	// 起點與終點相同會讓 isNight 永遠為真（跨午夜那條分支），等於整天都是夜間
	if c.NightStart == c.NightEnd {
		c.NightStart, c.NightEnd = Default().NightStart, Default().NightEnd
	}

	// 下限保護：間隔太短會讓「隨機化」失去意義，反而變成穩定的高頻打點
	const floor = 30 * time.Second
	if c.DayMin < floor {
		c.DayMin = floor
	}
	if c.DayMax <= c.DayMin {
		c.DayMax = c.DayMin + time.Second
	}
	if c.NightMin < floor {
		c.NightMin = floor
	}
	if c.NightMax <= c.NightMin {
		c.NightMax = c.NightMin + time.Second
	}
	return c
}

func envSecs(getenv func(string) string, key string, def time.Duration) time.Duration {
	v := getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return time.Duration(n) * time.Second
}

// envHour 讀 0–23 的整點。超出範圍或讀不懂就保持預設 ——
// 貼文量的分布會隨社團改變，這個邊界應該能調而不必改程式。
func envHour(getenv func(string) string, key string, def int) int {
	v := getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 23 {
		return def
	}
	return n
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
