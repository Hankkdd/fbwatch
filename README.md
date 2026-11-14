# fbwatch

A second-hand marketplace monitor. It watches Facebook groups and 7-11 myship
storefronts, pushes anything new to Discord within a minute with the full post
text, and exposes what it collected to a price-comparison bot.

Neither source gives you a usable alert. For Facebook groups, the "All posts"
setting does not cover priced listings, Marketplace saved-search alerts were
removed in 2024, and the Groups API read endpoints closed the same year.
7-11 myship — the consignment storefronts Taiwanese sellers use for payment and
shipping — has no notification feature at all. Polling is what is left.

```
  ┌───────────────────────────────────────────────┐
  │ collector                                     │
  │                                               │
  │  Facebook groups ─┐                           │
  │  (Chrome via CDP) │                           │
  │                   ├─▶ parse ─▶ ┌──────────┐   │
  │  7-11 myship ─────┘            │ Postgres │   │──▶ Discord
  │  (plain HTTP)                  └────┬─────┘   │    per-group routing
  │                                     │         │
  │  photos ──────────────────▶ MinIO   │         │
  └─────────────────────────────────────┼─────────┘
                                        │ read-only role
                                  ┌─────▼─────┐
                                  │    mcp    │◀── price-comparison bot
                                  └───────────┘    (separate project)
```

Three jobs, deliberately separated. The collector only fetches and stores —
it is the most fragile part of the system and the part that should change
least, so nothing is allowed to depend on its internals. Notification,
filtering and dedup read from Postgres. The MCP server is how an agent asks
"what is this thing actually selling for" without ever touching Facebook.

The two sources are independent: myship needs no login, no browser and no
session, so when Facebook breaks, storefront tracking keeps working.

## What it does

| | |
|---|---|
| **Poll** | 30–70 s during the day, 5–10 min overnight, randomised |
| **Parse** | Listing id, title, body, price, status, seller, timestamp, photos |
| **Full body** | Feed text is truncated; the permalink's embedded JSON is not |
| **Notify** | Discord webhook, body first, URL last — mobile push only shows the opening |
| **Highlight** | Keyword hits get one extra line on top; the post text is never altered |
| **Filter** | Per-group topic allow-list |
| **Route** | Per-group webhook, so different groups reach different channels |
| **Dedup** | Same seller, same content, same channel → notified once |
| **Shops** | 7-11 myship storefronts: new, sold out, relisted, price changed |
| **Alert** | Login wall, parser breakage, stalls, dead sources — all page you |

## Components

| Binary | Role |
|--------|------|
| `collect` | Poll → parse → store → notify. The only thing that touches Facebook |
| `mcp` | Read-only MCP server so an external agent can query what was collected |
| `dumpdom` | Save the rendered DOM and a screenshot, for when Facebook changes |
| `analyze` | Re-run the parser over saved DOM dumps |
| `probe` | Re-locate DOM anchors after a Facebook redesign |

## Read this first

**Scraping Facebook violates its Terms of Service.** There is no ambiguity
here. Your account may be checkpointed or disabled.

- **Use a throwaway account.** It only needs to have joined the target groups.
- The default pace is already conservative. Do not speed it up: measured
  end-to-end latency bottoms out around 7 minutes, and that floor is Facebook's
  own propagation delay, not your polling interval.
- On a login wall or checkpoint the collector **stops immediately and alerts**.
  It does not retry — a retry loop is what turns a soft block into a hard one.

You will also be storing other people's posts: names, photos, prices, contact
details. That is personal data belonging to the members of a private group.
Keep it to yourself, and delete it when you are done.

## Prerequisites

- Docker with Compose v2 (Go 1.26 only if you want to run the tests)
- 4 GB RAM (Chrome alone wants 2 GB)
- ~20 GB/year of disk, mostly listing photos
- x86-64 or arm64
- A Discord webhook, and a Facebook account that has joined the groups

## Quick start

```bash
git clone <this repo> fb-group-watch
cd fb-group-watch
./scripts/setup.sh
```

`setup.sh` asks for a Discord webhook and the groups to watch, generates
credentials, and writes `.env` and `config/seed.yaml`. It refuses to overwrite
either if they already exist.

```bash
docker compose up -d --build
# Creates postgres, minio and collector. The MCP service stays off by default.
```

Then log in to Facebook once, by hand:

```
http://127.0.0.1:6080/vnc.html
```

That opens the container's Chrome over noVNC. The session persists in
`chrome-profile/`, so this is a one-time step. Keep `BIND_IP` on localhost or a
private overlay — behind that port is a browser already signed in to your
account.

Finally, set `FBWATCH_LOOP=1` in `.env` and restart to begin polling.

```bash
docker compose up -d
docker compose logs -f collector
# 2026/11/14 16:23:22 [100000000000001] 解析 2 則，新增 0 則 (6.8s)
# 2026/11/14 16:23:23 下次輪詢 48s 後
```

Log output, code comments and the design notes are in Traditional Chinese.

## Configuration

`config/seed.yaml` is applied **only when the matching table is empty**. After
that the database is authoritative — disabling a group in SQL will not be undone
by the file on the next restart.

```yaml
groups:
  - id: "000000000000000"
    name: Example group
  - id: "111111111111111"
    webhook_env: DISCORD_WEBHOOK_URL_B    # this group goes to another channel
    require_patterns: [限量, 預購]         # and only when a post matches

keywords:
  - label: 限量
    line: Limited edition spotted
    priority: 10
    patterns: [限量, 限定, LE, Limited]

shops:
  - id: GM0000000000000
    name: Example storefront
```

Live changes go straight to the database:

```sql
UPDATE groups   SET enabled = FALSE          WHERE id = '...';
UPDATE keywords SET line    = 'grab this'    WHERE label = '限量';
UPDATE groups   SET require_patterns = ARRAY['顏料','噴槍'] WHERE id = '...';
```

Polling cadence, per-cycle fetch limits and the night window are environment
variables; see `.env.example`.

### Optional: the MCP service for a price-comparison bot

A notification carries text and a link. A bot cannot judge a listing from that —
many posts say only "as per title", and the photos are not in the message.

`cmd/mcp` is the integration point: reply to a notification in Discord, tag the
bot, and it queries back through these tools to tell you whether the asking
price is reasonable. It reads the data fbwatch already collected, through a
read-only database role, behind a bearer token, on a Docker network only. It
never touches Facebook itself.

The bot is a separate project; fbwatch only provides this interface.

| Tool | Input | Output |
|---|---|---|
| `listing` | post URL or id | title, price, body, location, seller, 4 photos (up to 8) |
| `comments` | post URL or id | comment thread, with the seller's own replies marked |
| `shop` | storefront id | live item list |
| `market` | keywords | ~120 days of asking prices across groups and storefronts |
| `fx` | currency | mid-market rate to TWD, cached |
| `video` | YouTube URL | transcript, captions or speech recognition |

Off by default. Set `COMPOSE_PROFILES=mcp` plus the two secrets in `.env`.

## When it breaks

It will break. The design assumes that, and prefers stopping loudly over
running quietly:

| Condition | Response |
|---|---|
| Login wall or checkpoint | Stop polling, alert, do not retry |
| 3 consecutive fetch failures | Cool down 30 min, retry once, then stop |
| 6 hours with no new items | Alert — the parser may have been broken by a redesign |
| Main loop stalled > 5 min | Alert and exit so the container restarts |
| Storefront unreachable | Alert after 3 / 5 / 15 cycles depending on the failure class |

A tripped breaker does not kill the process: noVNC stays reachable so you can
log in again.

**A Facebook redesign breaking the parser** is the common failure. Symptoms are
"parsed 0" or the six-hour silence alert. Every post keeps its `raw_html`, and
`parser_version` records which rows were parsed by which version, so a fixed
parser can be replayed over stored data instead of waiting for the posts to
reappear.

## Development

```bash
go test ./...
```

Database tests need a **separate** database. Pointed at the live one, their
fixtures would be picked up as new posts and pushed to Discord:

```bash
docker compose exec postgres psql -U fbwatch -d postgres \
  -c 'CREATE DATABASE fbwatch_test OWNER fbwatch;'
FBWATCH_TEST_DSN='postgres://fbwatch:<pw>@localhost:55432/fbwatch_test?sslmode=disable' \
  go test ./internal/store/
```

Without `FBWATCH_TEST_DSN` those tests **skip silently** and the run still
looks green.

Schema changes are plain SQL in `internal/store/migrations/`, applied in
filename order on every boot, so each one must be idempotent. They define
schema only — no seed data, since a migration runs on every user's database.

### Boot on startup

`scripts/fbwatch.service` is a systemd unit template; `User`, `Group` and
`WorkingDirectory` need your values. Why `restart: unless-stopped` is not
sufficient on its own is explained in that file.

### Do not commit

Covered by `.gitignore`, but the reasons are worth knowing:

- `chrome-profile*/` — a live Facebook session
- `.env*` — database, VNC and MinIO credentials, Discord webhooks
- `config/*.yaml` — which groups and storefronts you follow is itself personal
- `dumps/` — captured posts, containing other people's data

## Documentation

- [docs/design-notes.md](docs/design-notes.md) — why it is built this way.
  Mostly corrections made after something broke
- [docs/SPEC.md](docs/SPEC.md) — technical design and measurements
- [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md) — requirements and irreversible decisions

## Licence

MIT, see [LICENSE](LICENSE). Use at your own risk.
