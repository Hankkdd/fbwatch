// mcp 是給外部比價 bot 用的唯讀 MCP 服務。
//
// 比價 bot 看到 fbwatch 的通知時，只拿得到 Discord 上的文字和連結 ——
// 很多貼文內文只寫「售如標題」，照片也不在通知裡。這個服務讓它用連結查回
// fbwatch 已經存下來的標題、價格、內文與原圖。
//
// 安全邊界：
//   - FB 工具只查本地資料庫，用唯讀帳號，不會連到 FB
//   - 賣貨便工具只連 myship.7-11.com.tw，賣場代號必須符合 GM 加數字
//   - 行情工具只查本地資料庫；關鍵字的萬用字元會跳脫
//   - 匯率工具只連固定的匯率來源，模型只能選幣別
//   - 留言工具只傳資料庫查得到的貼文 ID 給 collector，網址由 collector 自己查
//   - 影片工具只收 YouTube 網址，交給 Agent Hub 抓逐字稿；Hub 的 token 不會交給模型
//   - 呼叫端（模型）給的永遠只是「代號」，不是可以任意指定的網址
package main

import (
	"context"
	"crypto/subtle"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"fbwatch/internal/blob"
	"fbwatch/internal/myship"
)

func main() {
	ctx := context.Background()

	dsn := mustEnv("FBWATCH_MCP_DSN")
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	images, err := blob.Open(ctx, mustEnv("MINIO_ENDPOINT"), mustEnv("MINIO_USER"),
		mustEnv("MINIO_PASSWORD"), envOr("MINIO_BUCKET", "listing-images"))
	if err != nil {
		log.Fatal(err)
	}

	t := &tools{db: pool, images: images, shops: myship.NewFetcher(), fxc: newFXCache()}

	no, yes := false, true
	server := mcp.NewServer(&mcp.Implementation{Name: "fbwatch", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name: "listing",
		Description: "查 fbwatch 存下來的 FB 社團拍賣貼文。給 FB 貼文連結或貼文 ID，" +
			"回傳標題、標價、內文、地區、賣家與商品照片。只查本地資料，不會連到 FB。",
		// 只讀本地資料庫，不連外
		Annotations: &mcp.ToolAnnotations{Title: "查 FB 拍賣貼文", ReadOnlyHint: true, OpenWorldHint: &no},
	}, t.listing)
	mcp.AddTool(server, &mcp.Tool{
		Name: "shop",
		Description: "查 7-11 賣貨便賣場的即時商品。給賣貨便連結或賣場代號（GM 開頭）。" +
			"連結帶 #商品編號 或另外給 item_id 時，只回傳那一件並附商品圖。",
		// 只讀，但會連到賣貨便
		Annotations: &mcp.ToolAnnotations{Title: "查賣貨便賣場", ReadOnlyHint: true, OpenWorldHint: &yes},
	}, t.shop)

	mcp.AddTool(server, &mcp.Tool{
		Name: "market",
		Description: "查社團行情：用品名關鍵字找 fbwatch 收錄過的 FB 社團貼文開價，以及賣貨便店家的開價。" +
			"中英文名各給一個命中率最高。這是台灣二手市場的實際開價，比搜尋到的零星價格可靠。",
		Annotations: &mcp.ToolAnnotations{Title: "查社團行情", ReadOnlyHint: true, OpenWorldHint: &no},
	}, t.market)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "fx",
		Description: "查外幣兌台幣匯率（市場中價，每天更新）。預設美金；給 amount 會直接換算成台幣。",
		Annotations: &mcp.ToolAnnotations{Title: "查匯率", ReadOnlyHint: true, OpenWorldHint: &yes},
	}, t.fx)
	// 留言要 collector 用已登入的瀏覽器現抓。collector 跟這個服務在同一個 compose 內網。
	t.cmts = newCommentsClient(envOr("FBWATCH_COMMENTS_URL", "http://collector:8090"), mustEnv("FBWATCH_MCP_TOKEN"))
	mcp.AddTool(server, &mcp.Tool{
		Name: "comments",
		Description: "讀 FB 社團貼文的留言（誰留言、回覆誰、內容，並標出哪則是賣家留的）。給 FB 貼文連結或 ID。" +
			"每次都即時開一次貼文頁，要等十幾秒。用在判斷是否已售出、有人喊單、賣家補充價格時。",
		Annotations: &mcp.ToolAnnotations{Title: "讀貼文留言", ReadOnlyHint: true, OpenWorldHint: &yes},
	}, t.comments)

	// 影片工具要 Agent Hub。沒設就不提供，其他工具照常運作。
	if base, token := os.Getenv("AGENT_HUB_URL"), os.Getenv("AGENT_HUB_TOKEN"); base != "" && token != "" {
		t.hub = newHubClient(base, token)
		mcp.AddTool(server, &mcp.Tool{
			Name: "video",
			Description: "讀 YouTube 影片的逐字稿（有字幕用字幕，沒有就語音辨識）。給影片網址，回傳標題與帶時間標記的逐字稿。" +
				"長影片第一次可能要等幾分鐘；回「還在抓」就稍後用同一個網址再呼叫。",
			Annotations: &mcp.ToolAnnotations{Title: "讀影片逐字稿", ReadOnlyHint: true, OpenWorldHint: &yes},
		}, t.video)
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)

	mux := http.NewServeMux()
	// 必填。這個檢查原本在 compose 的 ${...:?} 裡，搬過來是因為那個寫法會讓
	// 整份 compose 檔在 interpolation 階段失敗，連不要這個服務的人都起不來。
	mux.Handle("/mcp", requireToken(mustEnv("FBWATCH_MCP_TOKEN"), handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	addr := envOr("FBWATCH_MCP_ADDR", ":8080")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Printf("fbwatch mcp listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

// requireToken 要求 Authorization: Bearer。
//
// 服務只在 docker 內網，token 是第二道防線：之後有別的容器接上同一個網路，
// 也不會順手就能讀到社團資料。
//
// 沒有「token 是空字串就放行」這條路。那是設定出錯的樣子，而設定出錯時
// 該整個關上，不是整個打開 —— 症狀會是「查不到」，不是「誰都查得到」。
func requireToken(token string, next http.Handler) http.Handler {
	if token == "" {
		// 光是拿掉放行分支不夠：want 會變成 "Bearer "，對方照著送就通過。
		// main 用 mustEnv 擋在前面，這裡是第二道。
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("請設定 %s", k)
	}
	return v
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
