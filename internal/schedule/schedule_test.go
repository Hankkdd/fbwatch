package schedule

import (
	"testing"
	"time"
)

func TestNightWindowWrapsMidnight(t *testing.T) {
	c := Default() // 01:00–06:00 為夜間
	cases := []struct {
		hour  int
		night bool
	}{
		{0, false}, {1, true}, {3, true}, {5, true},
		{6, false}, {12, false}, {23, false},
	}
	for _, tc := range cases {
		if got := c.isNight(tc.hour); got != tc.night {
			t.Errorf("%02d 時 isNight = %v, want %v", tc.hour, got, tc.night)
		}
	}
}

func TestNextStaysInRange(t *testing.T) {
	c := Default()
	day := time.Date(2026, 9, 20, 14, 0, 0, 0, time.Local)
	night := time.Date(2026, 9, 20, 3, 0, 0, 0, time.Local)

	for i := 0; i < 200; i++ {
		d := c.Next(day)
		if d < c.DayMin || d >= c.DayMax {
			t.Fatalf("日間間隔 %s 超出 [%s, %s)", d, c.DayMin, c.DayMax)
		}
		n := c.Next(night)
		if n < c.NightMin || n >= c.NightMax {
			t.Fatalf("夜間間隔 %s 超出 [%s, %s)", n, c.NightMin, c.NightMax)
		}
	}
}

// 固定間隔是機器人簽章，抖動必須真的有變化
func TestNextIsJittered(t *testing.T) {
	c := Default()
	at := time.Date(2026, 9, 20, 14, 0, 0, 0, time.Local)
	seen := map[time.Duration]bool{}
	for i := 0; i < 50; i++ {
		seen[c.Next(at)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("50 次只產生 %d 種間隔，抖動不足", len(seen))
	}
}
