-- 給 cmd/mcp（比價 bot 查貼文用）的唯讀帳號。
--
-- 比價 bot 讀的是任何人都能寫的貼文內容，就算它被貼文裡的指令騙到，
-- 資料庫這一層也只能讀不能改。
--
-- 這裡只建角色與權限，不設密碼：密碼不能進 git。
-- collector 啟動時若有 FBWATCH_MCP_PG_PASSWORD，會替它開啟登入並設定密碼。
DO $$
BEGIN
	IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'fbwatch_mcp') THEN
		CREATE ROLE fbwatch_mcp NOLOGIN;
	END IF;
END
$$;

GRANT CONNECT ON DATABASE fbwatch TO fbwatch_mcp;
GRANT USAGE ON SCHEMA public TO fbwatch_mcp;
GRANT SELECT ON listings, listing_images, images, sellers, groups TO fbwatch_mcp;
