-- 售價改成「還買得到的最低規格價」。
--
-- 原本取的是所有規格的最低價，不管那個規格是不是賣完了。
-- 「處決者型反重力坦克」同時掛著完整盒裝 $2700 與水貼 $30，水貼賣掉之後
-- 通知仍然報 $30 —— 2026-10-08 真的這樣發出去過一次。全賣場 148 件裡
-- 有 11 件報的是賣完規格的價格。
--
-- 跟 009 清庫存基準同樣的道理：既有的 price 全是舊基準，留著的話
-- 下一輪會噴出 11 則看起來像跳水又馬上回彈的假改價通知。
-- 比對本來就會跳過沒有基準的商品，清掉等於安靜重建。
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_description d
        JOIN pg_class c ON c.oid = d.objoid
        WHERE c.relname = 'shop_items' AND d.description LIKE '%買得到的最低規格價%'
    ) THEN
        UPDATE shop_items SET price = NULL;
        COMMENT ON TABLE shop_items IS
            'price 是還買得到的最低規格價；整件賣完才退回最後的價格';
    END IF;
END $$;

COMMENT ON COLUMN shop_events.kind IS
    'new | relist | price | restock | soldout | removed；'
    'relist 是整件賣完被賣貨便藏起來、賣家補貨後又冒出來的舊商品，不是新上架';
