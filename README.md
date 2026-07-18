# ZYZU — 资源组 TG频道机器人

> **[yunmatai.xyz](https://yunmatai.xyz)** — TG自动化工具集 · 云控 · 采集 · 营销

自动抓取 [ziyuanzu.com](https://www.ziyuanzu.com) 77个影视资源站，下载视频并推送到Telegram频道。

## 架构

```
┌─ VPS ─────────────────────────────┐
│  zyzu server (API)                │
│  ├─ scraper → ziyuanzu.com        │
│  ├─ ffmpeg → mp4                  │
│  ├─ poster → TG频道               │
│  └─ :8080 REST API                │
└───────────────────────────────────┘
         ▲
         │ HTTP + API Key
         │
┌─ 本地 ────────────────────────────┐
│  zyzu-cli                         │
│  ├─ login  连接服务器              │
│  ├─ list   查看站点                │
│  ├─ block  屏蔽/解除               │
│  ├─ post   手动推送                │
│  └─ status 调度状态                │
└───────────────────────────────────┘
```

## 快速开始

**Server (VPS):**

```bash
git clone https://github.com/madtoby2/zyzu.git
cd zyzu
cp config.json.example config.json
vim config.json  # 填 bot_token, channel_id, api_key

# Docker (自带ffmpeg)
docker compose up -d

# 或直接运行
apt install ffmpeg
go run ./cmd/zyzu/
```

**Client (本地):**

```bash
# 从release下载或编译
make build-cli
./zyzu-cli login   # 输入服务器地址和API Key
./zyzu-cli list    # 查看所有站
./zyzu-cli block some-slug    # 屏蔽
./zyzu-cli post some-slug     # 手动推送到频道
./zyzu-cli status             # 查看调度状态
```

环境变量:
```bash
export ZYZU_SERVER=http://your-vps:8080
export ZYZU_KEY=your-api-key
```

## 配置

```json
{
  "api_key": "your-secret-key",
  "bot_token": "123456:ABC...",
  "channel_id": -1001234567890,
  "content_mode": "video",
  "content_limit": 5,
  "scrape_cron": "0 */6 * * *",
  "content_cron": "0 8,20 * * *"
}
```

## Server API

`/api/*` 读接口公开，写接口需 `X-API-Key` header。

| 方法 | 端点 | 说明 |
|------|------|------|
| GET | /health | 健康检查 |
| GET | /api/stations | 所有站点 |
| GET | /api/stations/stats | 统计 |
| GET | /api/status | 调度状态 |
| GET | /api/history | 推送历史 |
| POST | /api/stations/{slug}/blacklist | 屏蔽/解除 |
| POST | /api/stations/{slug}/post | 手动推送 |
| POST | /api/trigger | 立即采集 |
| POST | /api/content/trigger | 立即抓内容 |
| GET | /api/config | 查看配置 |
| PUT | /api/config | 修改配置 |

## 技术栈

Go + chi + pure-Go SQLite + ffmpeg + Docker

---

**[yunmatai.xyz](https://yunmatai.xyz)** — 更多TG自动化工具
