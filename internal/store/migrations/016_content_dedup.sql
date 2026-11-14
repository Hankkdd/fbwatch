-- 同內容去重。
--
-- 要擋的是「同一個賣家、同樣的內容、同一個通知頻道」重複推播。實測一個月：
--   跨社團 331 組 / 353 則多餘通知（賣家同時貼到兩個社團，中位數相隔 11 秒）
--   同社團 27 組 / 51 則（賣家隔天重貼同一批商品）
--
-- 兩把鍵，因為它們回答不同的問題：
--
--   feed_key    從第一眼的 feed 文字算。feed 是截斷的（平均 53 字，完整內文
--               平均 149 字），所以它只能當「疑似重複」的早期訊號，不能當判準 ——
--               實測有賣家用模板發文，兩則不同商品的 feed 前綴一模一樣。
--   content_key 從補抓到的完整內文算，這才是判準。
--
-- 這支 migration 取代了從未進版控的 007：欄位與索引當初跑過留在正式資料庫裡，
-- 但檔案不在 repo，重建環境就不會有。全部寫成 IF NOT EXISTS，兩種情況都能套用。
ALTER TABLE listings ADD COLUMN IF NOT EXISTS feed_key     TEXT;
ALTER TABLE listings ADD COLUMN IF NOT EXISTS content_key  TEXT;
ALTER TABLE listings ADD COLUMN IF NOT EXISTS duplicate_of TEXT;

CREATE INDEX IF NOT EXISTS listings_feed_key ON listings (seller_id, feed_key)
    WHERE feed_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS listings_content_key ON listings (seller_id, content_key)
    WHERE content_key IS NOT NULL;
CREATE INDEX IF NOT EXISTS listings_duplicate_of ON listings (duplicate_of)
    WHERE duplicate_of IS NOT NULL;

-- 清掉 007 留下的舊 content_key。
--
-- 那批值（127 筆）是用 feed 文字算的 —— 在新的語意下 content_key 必須來自
-- 完整內文。留著會拿錯的依據去比對，而且錯的方向是誤擋。
--
-- migration 每次開機都會重跑，所以用欄位註解當一次性的標記。
DO $$
DECLARE marker TEXT;
BEGIN
    SELECT col_description('listings'::regclass, a.attnum) INTO marker
    FROM pg_attribute a
    WHERE a.attrelid = 'listings'::regclass AND a.attname = 'content_key';

    IF marker IS DISTINCT FROM 'v2:full-body' THEN
        UPDATE listings SET content_key = NULL WHERE content_key IS NOT NULL;
        COMMENT ON COLUMN listings.content_key IS 'v2:full-body';
    END IF;
END $$;

-- 不回填既有貼文的 content_key：正規化規則寫在 Go 裡（strings.Fields），
-- 用 SQL 重寫一份一定會有出入，而兩種算法算出的鍵永遠對不上，
-- 錯得安靜。要擋的主力是跨社團重複，那兩份都是新貼文、都會拿到鍵，
-- 所以不回填也能從第一天就生效；只有「隔很多天重貼對上舊貼文」這種
-- 少數情況要等資料累積。
