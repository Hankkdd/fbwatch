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

func TestFromEnvOverrides(t *testing.T) {
	env := map[string]string{
		"FBWATCH_POLL_DAY_MIN":   "45",
		"FBWATCH_POLL_DAY_MAX":   "120",
		"FBWATCH_POLL_NIGHT_MIN": "300",
	}
	c := FromEnv(func(k string) string { return env[k] })

	if c.DayMin != 45*time.Second || c.DayMax != 120*time.Second {
		t.Errorf("日間 = %s–%s, want 45s–120s", c.DayMin, c.DayMax)
	}
	if c.NightMin != 300*time.Second {
		t.Errorf("夜間下限 = %s, want 300s", c.NightMin)
	}
	if c.NightMax != Default().NightMax {
		t.Errorf("未設定的項目應保持預設，實得 %s", c.NightMax)
	}
}

// 間隔太短會讓隨機化失去意義，變成穩定的高頻打點 —— 那正是機器人簽章。
func TestFromEnvEnforcesFloor(t *testing.T) {
	env := map[string]string{"FBWATCH_POLL_DAY_MIN": "2", "FBWATCH_POLL_DAY_MAX": "3"}
	c := FromEnv(func(k string) string { return env[k] })
	if c.DayMin < 30*time.Second {
		t.Errorf("DayMin = %s，應被下限保護在 30s 以上", c.DayMin)
	}
	if c.DayMax <= c.DayMin {
		t.Errorf("DayMax(%s) 必須大於 DayMin(%s)", c.DayMax, c.DayMin)
	}
}

func TestFromEnvIgnoresGarbage(t *testing.T) {
	env := map[string]string{"FBWATCH_POLL_DAY_MIN": "abc", "FBWATCH_POLL_DAY_MAX": "-5"}
	c := FromEnv(func(k string) string { return env[k] })
	if c.DayMin != Default().DayMin || c.DayMax != Default().DayMax {
		t.Errorf("無效值應退回預設，實得 %s–%s", c.DayMin, c.DayMax)
	}
}
