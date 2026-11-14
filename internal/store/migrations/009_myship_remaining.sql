-- 修正庫存誤讀，並補上「商品被刪掉」這種變動。
--
-- 原本 shop_items.inventory 存的是 Cgds_Inventory（賣家填的上架量），
-- 那個數字在有人下單時動都不動。2026-10-08 實測：賣場 139 件裡
-- 已經有 42 件賣完了，我們卻全部記成有貨，完售通知開台至今一次都沒發出去。
-- 真正的剩餘量在 Inventory 欄位，解析器已改讀它。
--
-- 另一種「賣掉」是賣家直接把商品刪掉（多半是指名預訂品，買家付完款就下架）。
-- 同一天有 18 件這樣消失，拿名字去平台搜尋也是 0 筆，確認是真的移除。
-- 這種變動原本完全偵測不到：比對只跑在「本輪有抓到」的商品上。
ALTER TABLE shop_items ADD COLUMN IF NOT EXISTS removed_at TIMESTAMPTZ;

-- 下面這段只在第一次升級時跑一次（migration 每次開機都會重跑）。
--
-- inventory 清成 NULL 是刻意的：既有的值全是錯的基準，留著的話
-- 下一輪改讀正確欄位會一次噴出 42 則完售通知洗版。比對本來就會跳過
-- 沒有基準的商品，清掉等於讓下一輪重新建立正確基準、不發通知。
-- 同理，那 18 件早就消失的也直接標成已移除，不補發。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'shop_items' AND column_name = 'stock'
    ) THEN
        ALTER TABLE shop_items ADD COLUMN stock INTEGER;  -- 原始上架量，只供對照

        UPDATE shop_items SET inventory = NULL;

        UPDATE shop_items i SET removed_at = now()
        WHERE i.last_seen < (
            SELECT max(j.last_seen) FROM shop_items j WHERE j.shop_id = i.shop_id
        );
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS shop_items_live ON shop_items (shop_id)
    WHERE removed_at IS NULL;

COMMENT ON COLUMN shop_events.kind IS
    'new | price | restock | soldout | removed；removed 是賣家把商品刪掉，由 markRemoved 產生';
