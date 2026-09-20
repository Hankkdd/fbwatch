# FB 社團拍賣文監控 — Spec

## 1. 問題與目標

兩個私密 FB 社團會出現拍賣貼文，現有通知管道拿不到，導致錯過。

**目標**：新拍賣 listing 出現後 3 分鐘內收到手機推播，含標題、價格、直達連結。

**非目標**：
- 不做歷史資料蒐集、不做分析
- 不抓留言、不抓成員、不抓賣家 profile
- 不自動回覆、不自動下標，純通知

## 2. 為什麼只能輪詢

| 路線 | 狀態 |
|---|---|
| Graph API Groups 讀取端點 | 2024-04-22 從所有 API 版本移除，管理員也無法申請 |
| mbasic 輕量 HTML | 2024-12 關閉 |
| RSS 產生器 | 私密社團架構上不可能 |
| 社團「所有貼文」通知 | 標的是 listing 不是 post，通知設定涵蓋不到 |
| Marketplace 儲存搜尋通知 | 2024 移除，殘留按鈕不穩定觸發；即使觸發也批次延遲 15–30 分 |

已實測：社團通知已設為「所有貼文」仍無即時推播，且目標貼文帶價格欄位，確認為 listing 型態。

輪詢是唯一路徑。此作法違反 FB 服務條款，風險自負。

## 3. 執行環境與硬性限制

執行機為開發機本身：NVIDIA GX10（DGX Spark），Cortex-X925/A725 20 核，121 GB RAM，Ubuntu 24.04.4，**aarch64**。

已確認：
- 對外 IP 為 HiNet（AS3462）台中，屬住宅 ISP。**最大風險因素排除，不需要 exit node。**
- Go 1.26.5、Python 3.12.3 已安裝
- Chrome、Xvfb、x11vnc 未安裝
- X server 已在執行（`/tmp/.X11-unix` 有 X0、X10），但 `DISPLAY` 未設。仍使用獨立 Xvfb 以隔離

這幾條是風險預算，不是建議事項。違反任何一條，專案的風險收益就不成立。

- **出口必須是住宅 IP。** 已滿足。HiNet 動態 IP 會變動，但那本身是正常使用者行為，不構成 checkpoint 觸發因素。
- **不得使用固定間隔。** `*/2 * * * *` 這種模式本身就是機器人簽章。
- **偵測到 checkpoint 必須熔斷，不得重試。** 重試迴圈會把軟性封鎖升級成硬封鎖。
- **單次輪詢只載入時間序第一屏。** 不捲動、不分頁、不點進貼文。
- **Session 必須在執行機上誕生。** 不從其他機器匯入 cookie。

### 3.1 帳號切換點

Stage 1–2 用主帳號。那兩階段是人工觸發的一次性抓取，行為量極小，風險可忽略。

**Stage 3 開始排程輪詢之前切換到備用帳號。** 持續輪詢才是風險真正產生的地方，主帳號不該暴露在那裡——主帳號被封的代價是連社團都進不去。

備用帳號需先通過社團審核，這是 Stage 3 的前置作業，要提早開始。

## 4. 技術選型

### 4.1 語言與瀏覽器控制：Go + go-rod

Go 生態沒有 Patchright（那是 Python／Node 的修補版 Playwright）。候選：

| 方式 | 隱蔽性 | 維護成本 | 資源 |
|---|---|---|---|
| go-rod 驅動真實 Chrome + 持久 profile | 高——它就是真瀏覽器 | 中 | 重（~300MB RSS） |
| `net/http` + utls + 解析內嵌 JSON | 中 | 高 | 極輕 |
| playwright-go | 中 | 中 | 重 |

**選 go-rod 驅動系統安裝的真實 Chrome。**

理由：本專案是登入態操作，FB 已經知道帳號是誰，目標不是匿名而是「持續看起來是同一台已知裝置」。純 HTTP 方案要手工偽造 UA、`sec-ch-ua` 系列、`x-fb-lsd`、TLS ClientHello 等一整組指紋，任何一項不一致都比直接用真 Chrome 更可疑；而真 Chrome 的這些值天生就是對的。

配置：
- 驅動系統安裝的 Chrome，不用 go-rod 自行下載的 Chromium
- 固定 `--user-data-dir`，**永不使用乾淨 profile**
- headful 跑在 Xvfb 下，不用 headless 模式
- 時區、語系、視窗尺寸固定，與首次登入時一致

#### aarch64 注意事項

Google 於 2026-07-30 開始提供官方 arm64 Linux 建置（v150 stable），走 Google 的 apt repo，所以「用真實 Chrome」在這台機器上成立。

但兩點要注意：
- **Chrome for Testing 的 arm64 建置仍在進行中**，go-rod 的自動下載可能不可用。本專案本來就指定系統 Chrome，不受影響。
- **不要用 snap 版 Chromium。** Ubuntu 24.04 的 chromium／firefox 都只有 snap，snap 的沙箱封裝會讓 `--user-data-dir` 行為異常。走 Google apt repo 裝 Chrome 可避開，且能自動更新——版本落後本身就是一個指紋訊號。

`rod/lib/stealth` 可加，但要認清它不是主要防線——IP、節奏、profile 穩定性才是。

### 4.2 其他相依

| 用途 | 選擇 | 理由 |
|---|---|---|
| 資料庫 | PostgreSQL + `pgx` | |
| 設定檔 | YAML | |
| HTTP（推播用） | 標準庫 | |

**Host port 用 55432，不要用 5432。** 開發機的 5432 已被長期服務佔用（`127.0.0.1:5432`），不要嘗試搶佔或關掉它。

### 4.3 Session 生命週期

1. 首次：在開發機上開 Xvfb + VNC，人工登入一次，完成所有驗證
2. 之後：每次都重用同一個 profile dir，session 自然延續
3. 失效時：熔斷並告警，由人工重新登入，**不嘗試自動登入**

自動登入是高風險動作，頻率特徵遠高於正常使用者。

### 4.4 排程

常駐 daemon 自行排下次執行，非 cron。

| 時段 | 間隔 |
|---|---|
| 06:00–01:00 | 90–240 秒隨機 |
| 01:00–06:00 | 600–1200 秒隨機 |

深夜拉長是因為每兩分鐘重整到天亮不是人類行為。該時段延遲上限 20 分鐘，已確認可接受。

### 4.5 抓取

目標 URL：`https://www.facebook.com/groups/<gid>/?sorting_setting=CHRONOLOGICAL`

已實測有效：排序下拉會從「熱門商品」變成「新貼文」。買賣類社團的預設到達頁是「商品買賣」分頁，走演算法排序，第一屏是舊內容，所以這個參數是必要的。

單次流程：導向 → **`Page.bringToFront`** → 輪詢 DOM 直到內容出現 → 取 DOM → 關閉分頁。不做任何互動。

**`bringToFront` 不可省略。** 實測背景分頁 Chrome 不渲染，FB 的 feed 會永遠停在骨架狀態——等 60 秒也一樣。這是靜默失效：拿得到 DOM、沒有錯誤，但內容永遠是空的。

等待改用輪詢而非固定 sleep：反覆取 DOM 直到 `data-visualcompletion="loading-state"` 消失。取 DOM 不執行 JS、不發網路請求，輪詢成本極低。

### 4.5.1 三個社團

| ID | 名稱 | 隱私 |
|---|---|---|
| 150336012291472 | 小人模型跳蚤市場 | 公開 |
| 236748899853629 | Taiwan Wargame Miniature Marketplace | 私密 |
| 227104705681276 | 戰錘warhammer 微縮模型殺肉交易所 | 私密 |

公開的那個理論上不需要登入態即可抓取。之後可考慮用獨立的無登入 context 處理它，把帳號曝險減少三分之一——但不是現在該做的優化。

### 4.6 解析

以下全部經 2026-09-20 實測確認。

**容器錨點：`div[data-virtualized]`**

不要用 `[role="article"]`——實測那些是尚未載入的骨架佔位符（內含 `aria-label="載入中……"`、`data-visualcompletion="loading-state"`），不是真實貼文。真實貼文單元由 FB 的虛擬列表以 `data-virtualized` 標記。

每個單元取：

| 欄位 | 來源 |
|---|---|
| ID | `/commerce/listing/(\d+)` |
| 賣家 | `/groups/\d+/user/(\d+)` |
| 內文 | `[data-ad-rendering-role="story_message"]` 子樹 |
| 連結 | `https://www.facebook.com/commerce/listing/<id>/` |
| 價格 | **內文**（見 4.6.1） |
| 狀態 | **內文**（見 4.6.1） |

**內文一定只取 `story_message` 子樹。** 整張卡片的文字含大量圖示與輔助標籤（實測開頭有 33 個重複的「Facebook」），直接取會嚴重污染。

#### 4.6.1 價格與狀態以內文為準

`NT$` 那個結構化欄位**不可信**，它常常是佔位值——實測「誠心收購」貼文標價 `NT$9,999`，那是社團慣例表示徵求，不是售價。真實價格寫在內文裡（「一張三百」「任務卡兩套組500」）。

所以：

- **價格主來源是內文**，`NT$` 欄位只作為次要訊號與交叉驗證
- 內文價格必須支援**中文數字**（「一張三百」= 300、「兩千五」= 2500）
- 狀態同樣由內文關鍵字判定

狀態列舉：

| 狀態 | 關鍵字 |
|---|---|
| `selling` | 預設 |
| `wanted` | 徵、徵求、收購、求購 |
| `sold` | 已售、售出、完售、已賣 |
| `reserved` | 保留、已保留 |

`NT$9,999` 同時作為 `wanted` 的輔助訊號。

#### 4.6.2 完整內文需要額外請求

實測確認內文在 feed 裡是**伺服器端截斷**的（「……」後直接接「查看更多」按鈕），完整內容不在 DOM 裡，CSS line-clamp 也救不了。

取得方式：請求該 listing 的 permalink 頁面。

代價可接受：**每則 listing 只做一次**（首次出現時），不是每次輪詢。以社團的貼文量推估一天數則，相對於 §3 的限制屬於低增量。但必須：

- 只對新 listing 抓，抓過就不再抓
- 與輪詢請求之間留隨機間隔，不要連續打
- 失敗就用截斷版內文，不重試

#### 已知限制

**單次只渲染約 2 則。** 視窗 1280x800 加上虛擬列表，一次只有視窗內的單元進 DOM。時間序排序下最新的在最上面，對輪詢而言足夠，但若社團在一次輪詢間隔內出現超過 2 則就會漏。視窗加大到 1920x1080 可多渲染幾則。

### 4.7 狀態與去重

```sql
CREATE TABLE listings (
  id           TEXT PRIMARY KEY,        -- commerce listing id
  group_id     TEXT NOT NULL,
  seller_id    TEXT,
  body         TEXT,                    -- 完整內文（取得後回填）
  body_feed    TEXT,                    -- feed 上的截斷版
  truncated    BOOLEAN NOT NULL,
  body_fetched BOOLEAN NOT NULL DEFAULT FALSE,
  price        INTEGER,                 -- 由內文判定
  nt_tag       INTEGER,                 -- 結構化 NT$ 欄位，不可信
  status       TEXT NOT NULL,           -- selling|wanted|sold|reserved
  permalink    TEXT,
  first_seen   TIMESTAMPTZ NOT NULL,
  last_seen    TIMESTAMPTZ NOT NULL,
  parser_version INTEGER NOT NULL
);
```

`parser_version` 讓解析器改版後能辨識哪些列該用新版重跑，不必全量重解析。

首次執行對每個社團只 seed 不通知，否則第一次會噴出整頁推播。

### 4.8 容器化

| 服務 | 內容 |
|---|---|
| `postgres` | 官方 image，host port **55432** |
| `collector` | Go binary + Chrome + Xvfb + openbox + x11vnc/noVNC |

需要持久化的 volume：

- **Chrome profile** —— 登入態在這裡，掉了就要重新人工登入
- **Postgres data**
- **圖片儲存**（R5）

#### 容器化帶來的一個張力

§4.1 說要讓 Chrome 自動更新，因為版本落後本身是指紋訊號。但容器 image 會把版本釘死。

所以需要**定期重建 image**。重建時 Chrome 版本會跳動——這是正常使用者更新瀏覽器的行為，不是問題；真正的問題是幾個月不重建，變成一個版本嚴重落後的瀏覽器在持續輪詢。

#### 登入流程

首次登入仍需人工，所以 collector 容器要對外開 noVNC（綁 Tailscale 介面）。

**Profile 用 bind mount 沿用宿主機既有目錄，不要建新的。**

容器內外的字型清單確實有差異，但這個差異遠不如「重新登入」來得危險：FB 會主動挑戰新裝置登入，卻不會因為既有 session 的字型清單變動而挑戰。**能不產生登入事件就不要產生。**

代價是容器的 UID 必須與 profile 目錄擁有者一致（本機為 1002），由 build arg `UID` 指定。

### 4.8 通知

ntfy，用 JSON 發布端點而非 header 帶參數——ntfy 的 header 只吃 ASCII，中文標題會壞。

```json
{"topic": "...", "title": "...", "message": "...", "click": "<permalink>", "priority": 5}
```

過濾條件（皆可選）：關鍵字白名單、價格上限。

### 4.9 熔斷與告警

| 觸發條件 | 動作 |
|---|---|
| 導向到登入頁或 `/checkpoint/` | 立即停止 daemon，推播告警，等人工處理 |
| 連續 3 次抓取失敗 | 暫停 30 分鐘後重試一次，再失敗則熔斷 |
| 連續 6 小時零新項目 | 推播提醒 |

最後一條是靜默失效偵測。解析器被 FB 改版打壞時，系統看起來一切正常但永遠不會通知，這比明顯崩潰更危險。夜間時段要把門檻拉長避免誤報。

## 5. 專案結構

```
cmd/fbwatch/main.go
internal/browser/    go-rod 封裝、profile 管理
internal/parse/      DOM → listing
internal/store/      SQLite
internal/notify/     ntfy
internal/schedule/   間隔抖動、quiet hours、熔斷
config.yaml
```

設定項：

```
groups:          社團 ID → 顯示名稱
ntfy_server / ntfy_topic
poll:            日間／夜間 min-max 秒數、quiet_hours 定義
filters:         keywords[], max_price
chrome_path / profile_dir
db_path
```

## 6. 已知失敗模式

| 失敗 | 徵兆 | 對策 |
|---|---|---|
| FB 改版打壞解析 | 零新項目但社團有貼文 | 6 小時靜默告警 |
| Session 過期 | 導向登入頁 | 熔斷 + 告警，人工重登 |
| 帳號被 checkpoint | 導向 checkpoint | 熔斷，不重試 |
| 重整時 feed 未載完 | 抓到空卡片 | 等渲染完成再取 DOM，計入失敗 |
| listing 編輯後重發 | 重複通知 | ID 為主鍵，天然去重 |

## 7. 分階段實作

每階段完成停下來 review 再繼續。

- ~~**Stage 1**：Xvfb + go-rod + 持久 profile，人工登入，抓取並 dump DOM~~ **完成 2026-09-20**
- ~~**Stage 2a**：解析器（容器錨點、內文、價格、狀態）+ 單元測試~~ **完成 2026-09-20**
- **Stage 2b**：完整內文抓取（請求 listing permalink）+ 測試
- **Stage 2c**：容器化 + Postgres schema + 去重寫入，輸出到 log
- **Stage 3**：切換備用帳號。daemon 排程 + 熔斷邏輯。跑一天觀察穩定性與帳號狀態。
- **Stage 4**：ntfy／Discord 通知 + 過濾條件。

## 8. 前置作業

- ~~確認對外 IP 屬性~~ 已確認為 HiNet 住宅 IP，不需 exit node
- 安裝 Chrome（Google apt repo，arm64）、Xvfb、x11vnc
- 備用帳號申請入社團，Stage 3 之前要完成審核
