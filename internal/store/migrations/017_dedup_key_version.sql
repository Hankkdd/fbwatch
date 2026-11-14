-- 去重的鍵改成會借標題，舊鍵全部作廢。
--
-- 起因：同一個賣家在 4 分鐘內把同一件商品貼到兩個社團，兩則都通知了。
--   標題幾乎一樣（差一個字），內文一則是「店到店運費60」、一則是
--   「店到店，加60運費。」—— 逐字不同，而且前者只有 7 個字連鍵都算不出來。
--
-- 商品卡片的內文常常只是運送資訊，短到算不出鍵：1134 則有內文的貼文裡有
-- 28 則（2.5%）因此完全不參與比對。這支要修的是這個。
--
-- 規則是「內文夠長就只看內文，短到算不出鍵時才借標題」，不是無條件併入標題。
-- 無條件併入試過也量過：同賣家、內文逐字相同但標題不同的有 14 組 / 31 則，
-- 翻開來看幾乎都是同一件東西被重貼 —— 標題打錯一個字母、補上「已售出」、
-- 同樣的意思換個說法。把標題算進去會讓這些全變成不同的鍵，等於多送 17 則
-- 重複通知；而「標題不同、內文是共用的運送說明、商品其實不一樣」這個情境，
-- 在 1206 則裡一次都沒出現。
--
-- 開頭那一組仍然擋不下來：兩邊標題也差一個字，逐字雜湊抓不到近似重複，
-- 而且從資料上無法判定那究竟是同一件商品，還是兩款名字相近的不同商品。
--
-- 舊鍵必須清掉而不是留著：不同算法算出的鍵永遠對不上，留著只會讓比對安靜地
-- 失效。清掉之後 feed_key 會在下一輪輪詢重新算出來（upsert 用 COALESCE，
-- 空的才寫），content_key 則只有尚未補抓內文的貼文會有 —— 跟 016 一樣不回填，
-- 理由也一樣：正規化規則寫在 Go 裡，用 SQL 重寫一份一定會有出入。
--
-- 標記放在 feed_key 的欄位註解，不動 content_key 的那個：016 每次開機都會檢查
-- content_key 的註解是不是 'v2:full-body'，共用一個欄位會讓兩支 migration
-- 每次開機互相把對方的標記蓋回去，每次都清空一次鍵。
--
-- 版號接在 016 的 v2 之後。v3 是「無條件併入標題」，上線過幾分鐘就被量掉了。
DO $$
DECLARE marker TEXT;
BEGIN
    SELECT col_description('listings'::regclass, a.attnum) INTO marker
    FROM pg_attribute a
    WHERE a.attrelid = 'listings'::regclass AND a.attname = 'feed_key';

    IF marker IS DISTINCT FROM 'v4:title-fallback' THEN
        UPDATE listings SET content_key = NULL, feed_key = NULL
        WHERE content_key IS NOT NULL OR feed_key IS NOT NULL;
        COMMENT ON COLUMN listings.feed_key IS 'v4:title-fallback';
    END IF;
END $$;
