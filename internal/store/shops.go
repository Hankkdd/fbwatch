package store

import (
	"context"
	"time"

	"fbwatch/internal/myship"
)

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
	OldPrice int
	NewPrice int
	OldQty   int
	NewQty   int
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

	for _, it := range items {
		price, qty := it.MinPrice(), it.Inventory()

		var oldPrice, oldQty *int
		var found bool
		err := s.pool.QueryRow(ctx,
			`SELECT price, inventory FROM shop_items WHERE shop_id=$1 AND item_id=$2`,
			shopID, it.ID).Scan(&oldPrice, &oldQty)
		switch {
		case err == nil:
			found = true
		case isNoRows(err):
			found = false
		default:
			return events, seeded, err
		}

		if !seeded {
			for _, e := range diffItem(found, oldPrice, oldQty, price, qty) {
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
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO shop_items (shop_id, item_id, name, description, price, inventory,
			                        status, image, created_at, first_seen, last_seen)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
			ON CONFLICT (shop_id, item_id) DO UPDATE SET
				name = EXCLUDED.name, description = EXCLUDED.description,
				price = EXCLUDED.price, inventory = EXCLUDED.inventory,
				status = EXCLUDED.status, image = EXCLUDED.image,
				last_seen = EXCLUDED.last_seen`,
			shopID, it.ID, nullableStr(it.Name), nullableStr(it.Description),
			nullableInt(price), qty, nullableStr(it.Status), nullableStr(it.Image),
			createdAt, now); err != nil {
			return events, seeded, err
		}
	}
	return events, seeded, nil
}

// diffItem 判斷這一輪相對上一輪有哪些值得通知的變動。
//
// 只看價格與庫存：名稱或描述的小修改不是買家在意的事，
// 通知出去只會變成雜訊。
func diffItem(found bool, oldPrice, oldQty *int, price, qty int) []string {
	if !found {
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
		SELECT e.id, e.shop_id, e.item_id, e.kind, COALESCE(i.name,''),
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
		if err := rows.Scan(&e.ID, &e.ShopID, &e.ItemID, &e.Kind, &e.Name,
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
