package store

import (
	"context"

	"fbwatch/internal/seed"
)

// SeedCounts 記錄這次實際種了多少，給啟動日誌用。
type SeedCounts struct{ Groups, Keywords, Shops int }

// ApplySeed 把設定檔的內容寫進資料庫，但只在對應的資料表是空的時候。
//
// 表一旦有內容就以表為準。兩邊同時有效的話，在資料庫停用一個社團，
// 下次重啟又會被設定檔加回來 —— 而「我明明關掉了它又自己開」是很難查的。
// 要改已經在跑的設定，就直接改資料庫；設定檔只負責第一次。
//
// 三張表各自判斷。加了賣場但還沒設關鍵字是正常狀態，不該因為 shops 有東西
// 就連 keywords 一起跳過。
func (s *Store) ApplySeed(ctx context.Context, f seed.File) (SeedCounts, error) {
	var c SeedCounts

	if len(f.Groups) > 0 {
		empty, err := s.tableEmpty(ctx, "groups")
		if err != nil {
			return c, err
		}
		if empty {
			for _, g := range f.Groups {
				if _, err := s.pool.Exec(ctx, `
					INSERT INTO groups (id, name, webhook_env, require_patterns)
					VALUES ($1, $2, $3, $4)
					ON CONFLICT DO NOTHING`,
					g.ID, nullableStr(g.Name), nullableStr(g.WebhookEnv),
					g.RequirePatterns); err != nil {
					return c, err
				}
				c.Groups++
			}
		}
	}

	if len(f.Keywords) > 0 {
		empty, err := s.tableEmpty(ctx, "keywords")
		if err != nil {
			return c, err
		}
		if empty {
			for _, k := range f.Keywords {
				priority := k.Priority
				if priority == 0 {
					priority = 100 // 跟資料表的預設一致
				}
				if _, err := s.pool.Exec(ctx, `
					INSERT INTO keywords (label, line, priority, patterns, summon)
					VALUES ($1, $2, $3, $4, $5)
					ON CONFLICT DO NOTHING`,
					k.Label, k.Line, priority, k.Patterns, k.Summon); err != nil {
					return c, err
				}
				c.Keywords++
			}
		}
	}

	if len(f.Shops) > 0 {
		empty, err := s.tableEmpty(ctx, "shops")
		if err != nil {
			return c, err
		}
		if empty {
			for _, sh := range f.Shops {
				// 寫 label 不寫 name：name 是賣場在來源上的顯示名稱、會被覆蓋，
				// 設定檔裡那個是「我怎麼叫它」，兩者得分開存。
				if _, err := s.pool.Exec(ctx, `
					INSERT INTO shops (id, label, note) VALUES ($1, $2, $3)
					ON CONFLICT DO NOTHING`,
					sh.ID, nullableStr(sh.Name), nullableStr(sh.Note)); err != nil {
					return c, err
				}
				c.Shops++
			}
		}
	}

	return c, nil
}

// tableEmpty 的表名由呼叫端寫死，沒有來自外部的字串。
func (s *Store) tableEmpty(ctx context.Context, table string) (bool, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
		return false, err
	}
	return n == 0, nil
}
