package store

import (
	"testing"

	"fbwatch/internal/seed"
)

func clearSeedTables(t *testing.T, st *Store) {
	t.Helper()
	_, ctx := st, t.Context()
	for _, q := range []string{`DELETE FROM listings`, `DELETE FROM groups`,
		`DELETE FROM keywords`, `DELETE FROM shops`} {
		if _, err := st.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
}

func TestApplySeedWritesEverySection(t *testing.T) {
	st, ctx := testStore(t)
	clearSeedTables(t, st)

	f := seed.File{
		Groups: []seed.Group{{
			ID: "g-seed-1", Name: "測試社團",
			WebhookEnv:      "DISCORD_WEBHOOK_URL_B",
			RequirePatterns: []string{"限量", "預購"},
		}},
		Keywords: []seed.Keyword{{
			Label: "限量", Line: "限量品出現了",
			Patterns: []string{"限量", "限定"}, Summon: true,
		}},
		Shops: []seed.Shop{{ID: "GM-seed-1", Name: "測試賣場"}},
	}
	c, err := st.ApplySeed(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if c != (SeedCounts{Groups: 1, Keywords: 1, Shops: 1}) {
		t.Fatalf("實得 %+v", c)
	}

	gs, err := st.EnabledGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].WebhookEnv != "DISCORD_WEBHOOK_URL_B" ||
		len(gs[0].RequirePatterns) != 2 {
		t.Errorf("分流與主題過濾也要一起寫進去，實得 %+v", gs)
	}

	// 賣場的自訂稱呼要進 label。寫進 name 的話，等來源的顯示名稱開始被寫入
	// 就會被蓋掉，而使用者只會發現「我取的名字不見了」。
	var label, name *string
	if err := st.pool.QueryRow(ctx,
		`SELECT label, name FROM shops WHERE id = 'GM-seed-1'`).Scan(&label, &name); err != nil {
		t.Fatal(err)
	}
	if label == nil || *label != "測試賣場" {
		t.Errorf("自訂稱呼要存進 label，實得 %v", label)
	}
	if name != nil {
		t.Errorf("name 留給來源的顯示名稱，不該由設定檔填，實得 %q", *name)
	}
}

// 表一旦有內容就以表為準。設定檔若持續有效，在資料庫停用一個社團之後
// 下次重啟又會被加回來 —— 那等於停用功能失效。
func TestApplySeedOnlyWhenTableEmpty(t *testing.T) {
	st, ctx := testStore(t)
	clearSeedTables(t, st)

	f := seed.File{Groups: []seed.Group{{ID: "g-seed-1"}, {ID: "g-seed-2"}}}
	if _, err := st.ApplySeed(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(ctx,
		`UPDATE groups SET enabled = FALSE WHERE id = 'g-seed-2'`); err != nil {
		t.Fatal(err)
	}

	f.Groups = append(f.Groups, seed.Group{ID: "g-seed-3"})
	c, err := st.ApplySeed(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if c.Groups != 0 {
		t.Fatalf("表已有內容就不該再套用，實得 %d", c.Groups)
	}

	gs, err := st.EnabledGroups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 1 || gs[0].ID != "g-seed-1" {
		t.Errorf("停用的社團不該被設定檔加回來，實得 %+v", gs)
	}
}

// 三張表各自判斷。先加了賣場、關鍵字還沒設，是正常狀態。
func TestApplySeedJudgesEachTableSeparately(t *testing.T) {
	st, ctx := testStore(t)
	clearSeedTables(t, st)

	if _, err := st.ApplySeed(ctx, seed.File{
		Shops: []seed.Shop{{ID: "GM-seed-1"}},
	}); err != nil {
		t.Fatal(err)
	}

	c, err := st.ApplySeed(ctx, seed.File{
		Shops:    []seed.Shop{{ID: "GM-seed-1"}},
		Keywords: []seed.Keyword{{Label: "限量", Line: "嗨", Patterns: []string{"限量"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Keywords != 1 {
		t.Errorf("shops 有內容不該擋住 keywords，實得 %d", c.Keywords)
	}
	if c.Shops != 0 {
		t.Errorf("shops 已有內容不該重複寫入，實得 %d", c.Shops)
	}
}
