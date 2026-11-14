package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"fbwatch/internal/keyword"
	"fbwatch/internal/myship"
)

// ErrIncompleteFetch 是這輪抓到的商品數掉太多，判定頁面沒回傳完整。
//
// 單獨一個 sentinel 而不是普通錯誤，因為它的意義跟其他失敗相反：
// 其他失敗是「這輪沒拿到資料」，這個是「拿到了但不敢採信，所以主動丟掉」。
// 連續丟掉才是真正要告警的事 —— 那代表要嘛抓取持續不完整，
// 要嘛賣家真的清了四成以上的貨，而保護機制正把真實的下架擋在外面。
var ErrIncompleteFetch = errors.New("抓取不完整")

type Shop struct {
	ID   string
	Name string
}

// EnabledShops 回傳要監控的賣貨便賣場。每輪重讀，改 enabled 即時生效。
func (s *Store) EnabledShops(ctx context.Context) ([]Shop, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, COALESCE(NULLIF(label,''), name, '') FROM shops WHERE enabled ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Shop
	for rows.Next() {
		var sh Shop
		if err := rows.Scan(&sh.ID, &sh.Name); err != nil {
			return nil, err
		}
		out = append(out, sh)
	}
	return out, rows.Err()
}

func (s *Store) AddShop(ctx context.Context, id, name, note string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO shops (id, name, note) VALUES ($1,$2,$3)
		ON CONFLICT (id) DO UPDATE SET name = COALESCE(NULLIF(EXCLUDED.name,''), shops.name)`,
		id, nullableStr(name), nullableStr(note))
	return err
}

type ShopEvent struct {
	ID       int64
	ShopID   string
	ItemID   string
	Kind     string
	Name     string
	Image    string
	OldPrice int
	NewPrice int
	OldQty   int
	NewQty   int
	Status   string // 商品保存情況：1 新品、2 二手
}

// SyncShopItems 寫入本輪抓到的商品，並與上一輪比對產生變動事件。
//
// 賣場第一次出現時只建基準、不產生事件 —— 否則首次執行會把整個賣場
// （實測 143 件）當成新上架全部通知出去。
func (s *Store) SyncShopItems(ctx context.Context, shopID string, items []myship.Item, now time.Time) (events int, seeded bool, err error) {
	var existing int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM shop_items WHERE shop_id = $1`, shopID).Scan(&existing); err != nil {
		return 0, false, err
	}
	seeded = existing == 0

	// 開始追蹤的時間，用來分辨「真的新上架」與「舊商品重新出現」
	var watchSince time.Time
	if err := s.pool.QueryRow(ctx,
		`SELECT added_at FROM shops WHERE id = $1`, shopID).Scan(&watchSince); err != nil {
		return 0, false, err
	}

	for _, it := range items {
		price, qty := it.Price(), it.Remaining()

		var oldPrice, oldQty *int
		var removedAt *time.Time
		var found bool
		err := s.pool.QueryRow(ctx,
			`SELECT price, inventory, removed_at FROM shop_items WHERE shop_id=$1 AND item_id=$2`,
			shopID, it.ID).Scan(&oldPrice, &oldQty, &removedAt)
		switch {
		case err == nil:
			found = true
		case isNoRows(err):
			found = false
		default:
			return events, seeded, err
		}

		preexisting := !it.CreatedAt.IsZero() && it.CreatedAt.Before(watchSince)

		if !seeded {
			for _, e := range diffItem(found && removedAt == nil, preexisting, oldPrice, oldQty, price, qty) {
				if err := s.insertShopEvent(ctx, shopID, it.ID, e, oldPrice, oldQty, price, qty, now); err != nil {
					return events, seeded, err
				}
				events++
			}
		}

		var createdAt *time.Time
		if !it.CreatedAt.IsZero() {
			c := it.CreatedAt
			createdAt = &c
		}
		// 重新出現的要把 removed_at 清掉，否則它會一直被當成已移除
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO shop_items (shop_id, item_id, name, description, price, inventory, stock,
			                        status, image, created_at, first_seen, last_seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11)
			ON CONFLICT (shop_id, item_id) DO UPDATE SET
				name = EXCLUDED.name, description = EXCLUDED.description,
				price = EXCLUDED.price, inventory = EXCLUDED.inventory,
				stock = EXCLUDED.stock,
				status = EXCLUDED.status, image = EXCLUDED.image,
				last_seen = EXCLUDED.last_seen, removed_at = NULL`,
			shopID, it.ID, nullableStr(it.Name), nullableStr(it.Description),
			nullableInt(price), qty, it.Stock(), nullableStr(it.Status), nullableStr(it.Image),
			createdAt, now); err != nil {
			return events, seeded, err
		}
	}

	// 頁面只回傳一部分商品時，剩下的會看起來像被刪光。
	// 實測連抓三次都穩定是 139 件，所以「一輪少掉四成以上」不是賣家在清貨，
	// 是這次抓取不完整 —— 寧可晚一輪偵測到移除，也不要噴出幾十則假通知。
	if !seeded && len(items) > 0 {
		var live int
		if err := s.pool.QueryRow(ctx,
			`SELECT count(*) FROM shop_items WHERE shop_id = $1 AND removed_at IS NULL`,
			shopID).Scan(&live); err != nil {
			return events, seeded, err
		}
		if live > 0 && len(items)*10 < live*6 {
			return events, seeded, fmt.Errorf(
				"%w：賣場 %s 這輪只抓到 %d 件、原有 %d 件", ErrIncompleteFetch, shopID, len(items), live)
		}
	}

	if !seeded {
		n, err := s.markRemoved(ctx, shopID, now)
		if err != nil {
			return events, seeded, err
		}
		events += n
	}
	return events, seeded, nil
}

// markRemoved 把這一輪沒再出現的商品標成已移除，並補上 removed 事件。
//
// 賣貨便上「賣掉」有兩種長相：庫存降到 0，或賣家直接把商品刪掉 ——
// 指名預訂品（「Mr.X 專拍」）幾乎都是後者，買家付完款就下架。
// 只比對本輪抓到的商品永遠看不到第二種，2026-10-08 那 18 件就是這樣漏掉的。
func (s *Store) markRemoved(ctx context.Context, shopID string, now time.Time) (int, error) {
	rows, err := s.pool.Query(ctx, `
		UPDATE shop_items SET removed_at = $2
		WHERE shop_id = $1 AND removed_at IS NULL AND last_seen < $2
		RETURNING item_id, price, inventory`, shopID, now)
	if err != nil {
		return 0, err
	}
	type gone struct {
		id    string
		price *int
		qty   *int
	}
	var list []gone
	for rows.Next() {
		var g gone
		if err := rows.Scan(&g.id, &g.price, &g.qty); err != nil {
			rows.Close()
			return 0, err
		}
		list = append(list, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, g := range list {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO shop_events (shop_id, item_id, kind, old_price, old_qty, detected_at)
			VALUES ($1,$2,'removed',$3,$4,$5)`, shopID, g.id, g.price, g.qty, now); err != nil {
			return 0, err
		}
	}
	return len(list), nil
}

// diffItem 判斷這一輪相對上一輪有哪些值得通知的變動。
//
// 只看價格與剩餘量：名稱或描述的小修改不是買家在意的事，
// 通知出去只會變成雜訊。商品被刪掉那種變動在這裡看不到，
// 因為它不在本輪抓到的清單裡，由 markRemoved 負責。
//
// preexisting 是「商品建檔時間早於我們開始追蹤」。賣貨便會把整件賣完的商品
// 從賣場頁藏起來，賣家補了貨又冒出來 —— 對我們看起來就是全新商品。
// 2026-10-08 的 23 則「新上架」裡有 16 則是這樣來的，而且點進去會發現
// 商品排在賣場第 108 位、建檔時間是七月，完全不像新上架。
func diffItem(found, preexisting bool, oldPrice, oldQty *int, price, qty int) []string {
	if !found {
		if preexisting {
			return []string{"relist"}
		}
		return []string{"new"}
	}
	var out []string
	if oldPrice != nil && price > 0 && *oldPrice != price {
		out = append(out, "price")
	}
	if oldQty != nil {
		switch {
		case *oldQty > 0 && qty == 0:
			out = append(out, "soldout")
		case *oldQty == 0 && qty > 0:
			out = append(out, "restock")
		}
	}
	return out
}

func (s *Store) insertShopEvent(ctx context.Context, shopID, itemID, kind string,
	oldPrice, oldQty *int, price, qty int, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO shop_events (shop_id, item_id, kind, old_price, new_price, old_qty, new_qty, detected_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		shopID, itemID, kind, oldPrice, nullableInt(price), oldQty, qty, now)
	return err
}

// PendingShopEvents 回傳尚未通知的變動，附上商品名稱。
func (s *Store) PendingShopEvents(ctx context.Context, limit int) ([]ShopEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT e.id, e.shop_id, e.item_id, e.kind, COALESCE(i.name,''), COALESCE(i.image,''), COALESCE(i.status,''),
		       COALESCE(e.old_price,0), COALESCE(e.new_price,0),
		       COALESCE(e.old_qty,0), COALESCE(e.new_qty,0)
		FROM shop_events e
		LEFT JOIN shop_items i ON i.shop_id = e.shop_id AND i.item_id = e.item_id
		WHERE e.notified_at IS NULL
		ORDER BY e.detected_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ShopEvent
	for rows.Next() {
		var e ShopEvent
		if err := rows.Scan(&e.ID, &e.ShopID, &e.ItemID, &e.Kind, &e.Name, &e.Image, &e.Status,
			&e.OldPrice, &e.NewPrice, &e.OldQty, &e.NewQty); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) MarkShopEventsNotified(ctx context.Context, ids []int64, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE shop_events SET notified_at = $2 WHERE id = ANY($1) AND notified_at IS NULL`,
		ids, now)
	return err
}

// MarkAllShopEventsNotified 把某賣場目前所有未通知的事件標成已通知，
// 用於首次建立基準後的保險。
func (s *Store) MarkAllShopEventsNotified(ctx context.Context, shopID string, now time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE shop_events SET notified_at = $2 WHERE shop_id = $1 AND notified_at IS NULL`,
		shopID, now)
	return err
}

// EnabledKeywords 回傳要比對的主題。每輪重讀，改表即時生效。
func (s *Store) EnabledKeywords(ctx context.Context) ([]keyword.Rule, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT label, line, priority, patterns, summon FROM keywords WHERE enabled ORDER BY priority, label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []keyword.Rule
	for rows.Next() {
		var label, line string
		var priority int
		var patterns []string
		var summon bool
		if err := rows.Scan(&label, &line, &priority, &patterns, &summon); err != nil {
			return nil, err
		}
		r := keyword.NewRule(label, line, priority, patterns)
		r.Summon = summon
		out = append(out, r)
	}
	return out, rows.Err()
}

// DisableShop 停止追蹤一個賣場，並把原因寫進 note。
//
// 用在賣家把賣場整個關掉：那不是暫時的錯誤，重試不會好，
// 繼續試只會每分鐘在日誌裡留一行沒人看的失敗。
func (s *Store) DisableShop(ctx context.Context, id, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE shops
		SET enabled = FALSE,
		    note = trim(both ' ' from COALESCE(note,'') || ' | ' || $2)
		WHERE id = $1 AND enabled`, id, reason)
	return err
}
