#!/usr/bin/env python3
"""Generate the built-in app catalog (internal/appstore/apps/<id>/{berth.yaml,compose.yaml}).

Edit APPS below and run:  python3 scripts/apps/gen.py
The verified block of an existing app is kept (scripts/apptest writes it after testing).
"""
import json, os, re, sys

ROOT = os.path.join(os.path.dirname(__file__), "..", "..", "internal", "appstore", "apps")

def L(cn, tw, en):
    return {"zh-CN": cn, "zh-TW": tw, "en": en}

PORT = lambda key, default, cn="端口", tw="連接埠", en="Port": {
    "key": key, "type": "port", "default": str(default), "required": True, "label": L(cn, tw, en),
    "hint": L("只在本机监听；对外访问请建站点", "只在本機監聽；對外存取請建立站點", "Listens on this host only; add a site to publish it")}
PUBPORT = lambda key, default, cn, tw, en: {  # ports other devices connect to directly
    "key": key, "type": "port", "default": str(default), "required": True, "label": L(cn, tw, en)}
PASS = lambda key="ADMIN_PASSWORD", cn="管理员密码", tw="管理員密碼", en="Admin password": {
    "key": key, "type": "password", "generate": True, "label": L(cn, tw, en),
    "hint": L("留空则自动生成强密码，安装后可在已装应用里查看", "留空則自動產生強密碼，安裝後可在已裝應用裡查看", "Leave empty to generate one; view it later under installed apps")}
TEXT = lambda key, default, cn, tw, en, req=True: {"key": key, "type": "string", "default": default, "required": req, "label": L(cn, tw, en)}

APPS = []

def app(id, name, summary, category, version, website, image_compose, *, mem, disk, fields, ports, health=None, site=None, arch=("arm64", "amd64")):
    APPS.append(dict(id=id, name=name, summary=summary, category=category, version=version, website=website,
                     compose=image_compose, mem_mb=mem, disk_mb=disk, fields=fields, ports=ports,
                     health=health, site=site, arch=list(arch)))

def web(svc, port):  # health + site on the same web port
    return dict(health={"service": svc, "port": port, "path": "/"}, site={"service": svc, "port": port})

# ---------------------------------------------------------------- tools (small)
app("it-tools", L("IT 工具箱", "IT 工具箱", "IT Tools"),
    L("开发和运维常用的小工具合集：编码转换、哈希、UUID、JWT、正则、颜色等", "開發與維運常用的小工具合集：編碼轉換、雜湊、UUID、JWT、正規表示式、顏色等", "Handy developer tools in one page: encoders, hashes, UUIDs, JWT, regex, colours"),
    "tools", "2024.10.22", "https://it-tools.tech", """services:
  it-tools:
    image: corentinth/it-tools:2024.10.22-7ca5933
    restart: unless-stopped
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
    mem_limit: 64m
""", mem=32, disk=60, fields=[PORT("HTTP_PORT", 8181)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}], **web("it-tools", 80))

# ---------------------------------------------------------------- databases
app("postgres", L("PostgreSQL", "PostgreSQL", "PostgreSQL"),
    L("功能完整的开源关系型数据库", "功能完整的開源關聯式資料庫", "The full-featured open-source relational database"),
    "database", "17", "https://www.postgresql.org", """services:
  postgres:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${DB_USER}
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: ${DB_NAME}
    volumes:
      - ${BERTH_DATA}/data:/var/lib/postgresql/data
    ports: ["127.0.0.1:${DB_PORT}:5432"]
    healthcheck: { test: ["CMD-SHELL", "pg_isready -U ${DB_USER}"], interval: 10s, retries: 5 }
""", mem=128, disk=300,
    fields=[TEXT("DB_USER", "app", "用户名", "使用者名稱", "User"), PASS("DB_PASSWORD", "密码", "密碼", "Password"),
            TEXT("DB_NAME", "app", "数据库名", "資料庫名稱", "Database"), PORT("DB_PORT", 5432)],
    ports=[{"name": "postgres", "port": 5432, "field": "DB_PORT"}])

app("mariadb", L("MariaDB", "MariaDB", "MariaDB"),
    L("兼容 MySQL 的开源数据库", "相容 MySQL 的開源資料庫", "Open-source MySQL-compatible database"),
    "database", "11.4", "https://mariadb.org", """services:
  mariadb:
    image: mariadb:11.4
    restart: unless-stopped
    environment:
      MARIADB_ROOT_PASSWORD: ${ROOT_PASSWORD}
      MARIADB_DATABASE: ${DB_NAME}
      MARIADB_USER: ${DB_USER}
      MARIADB_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/var/lib/mysql
    ports: ["127.0.0.1:${DB_PORT}:3306"]
    healthcheck: { test: ["CMD", "healthcheck.sh", "--connect", "--innodb_initialized"], interval: 10s, retries: 5 }
""", mem=256, disk=400,
    fields=[PASS("ROOT_PASSWORD", "root 密码", "root 密碼", "Root password"), TEXT("DB_NAME", "app", "数据库名", "資料庫名稱", "Database"),
            TEXT("DB_USER", "app", "用户名", "使用者名稱", "User"), PASS("DB_PASSWORD", "密码", "密碼", "Password"), PORT("DB_PORT", 3306)],
    ports=[{"name": "mariadb", "port": 3306, "field": "DB_PORT"}])

app("mysql", L("MySQL", "MySQL", "MySQL"),
    L("最流行的开源关系型数据库之一", "最流行的開源關聯式資料庫之一", "One of the most popular open-source databases"),
    "database", "8.4", "https://www.mysql.com", """services:
  mysql:
    image: mysql:8.4
    restart: unless-stopped
    environment:
      MYSQL_ROOT_PASSWORD: ${ROOT_PASSWORD}
      MYSQL_DATABASE: ${DB_NAME}
      MYSQL_USER: ${DB_USER}
      MYSQL_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/var/lib/mysql
    ports: ["127.0.0.1:${DB_PORT}:3306"]
""", mem=400, disk=600,
    fields=[PASS("ROOT_PASSWORD", "root 密码", "root 密碼", "Root password"), TEXT("DB_NAME", "app", "数据库名", "資料庫名稱", "Database"),
            TEXT("DB_USER", "app", "用户名", "使用者名稱", "User"), PASS("DB_PASSWORD", "密码", "密碼", "Password"), PORT("DB_PORT", 3307)],
    ports=[{"name": "mysql", "port": 3306, "field": "DB_PORT"}])

app("redis", L("Redis", "Redis", "Redis"),
    L("内存键值数据库，常用作缓存和队列", "記憶體鍵值資料庫，常用作快取與佇列", "In-memory key-value store for caches and queues"),
    "database", "7.4", "https://redis.io", """services:
  redis:
    image: redis:7.4-alpine
    restart: unless-stopped
    command: ["redis-server", "--appendonly", "yes", "--requirepass", "${REDIS_PASSWORD}"]
    volumes:
      - ${BERTH_DATA}/data:/data
    ports: ["127.0.0.1:${REDIS_PORT}:6379"]
""", mem=64, disk=100,
    fields=[PASS("REDIS_PASSWORD", "密码", "密碼", "Password"), PORT("REDIS_PORT", 6379)],
    ports=[{"name": "redis", "port": 6379, "field": "REDIS_PORT"}])

app("valkey", L("Valkey", "Valkey", "Valkey"),
    L("Redis 的开源分支，接口兼容", "Redis 的開源分支，介面相容", "Open-source fork of Redis, protocol compatible"),
    "database", "8", "https://valkey.io", """services:
  valkey:
    image: valkey/valkey:8-alpine
    restart: unless-stopped
    command: ["valkey-server", "--appendonly", "yes", "--requirepass", "${REDIS_PASSWORD}"]
    volumes:
      - ${BERTH_DATA}/data:/data
    ports: ["127.0.0.1:${REDIS_PORT}:6379"]
""", mem=64, disk=100,
    fields=[PASS("REDIS_PASSWORD", "密码", "密碼", "Password"), PORT("REDIS_PORT", 6380)],
    ports=[{"name": "valkey", "port": 6379, "field": "REDIS_PORT"}])

app("mongodb", L("MongoDB", "MongoDB", "MongoDB"),
    L("文档型数据库", "文件型資料庫", "Document database"),
    "database", "8.0", "https://www.mongodb.com", """services:
  mongodb:
    image: mongo:8.0
    restart: unless-stopped
    environment:
      MONGO_INITDB_ROOT_USERNAME: ${DB_USER}
      MONGO_INITDB_ROOT_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/data/db
    ports: ["127.0.0.1:${DB_PORT}:27017"]
""", mem=400, disk=500,
    fields=[TEXT("DB_USER", "root", "管理员用户名", "管理員使用者名稱", "Admin user"), PASS("DB_PASSWORD", "密码", "密碼", "Password"), PORT("DB_PORT", 27017)],
    ports=[{"name": "mongodb", "port": 27017, "field": "DB_PORT"}])

# ---------------------------------------------------------------- tools
app("adminer", L("Adminer", "Adminer", "Adminer"),
    L("单页数据库管理工具，支持 MySQL、PostgreSQL 等", "單頁資料庫管理工具，支援 MySQL、PostgreSQL 等", "Database manager in one page: MySQL, PostgreSQL and more"),
    "tools", "4.8.1", "https://www.adminer.org", """services:
  adminer:
    image: adminer:4.8.1
    restart: unless-stopped
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
""", mem=32, disk=50, fields=[PORT("HTTP_PORT", 8180)],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}], **web("adminer", 8080))

app("pgadmin", L("pgAdmin", "pgAdmin", "pgAdmin"),
    L("PostgreSQL 的图形化管理工具", "PostgreSQL 的圖形化管理工具", "Graphical administration for PostgreSQL"),
    "tools", "9", "https://www.pgadmin.org", """services:
  pgadmin:
    image: dpage/pgadmin4:9
    restart: unless-stopped
    environment:
      PGADMIN_DEFAULT_EMAIL: ${ADMIN_EMAIL}
      PGADMIN_DEFAULT_PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/var/lib/pgadmin
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
""", mem=256, disk=200,
    fields=[TEXT("ADMIN_EMAIL", "admin@example.com", "登录邮箱", "登入電子郵件", "Sign-in email"), PASS(), PORT("HTTP_PORT", 8181 + 1)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}], **web("pgadmin", 80))

app("dozzle", L("Dozzle", "Dozzle", "Dozzle"),
    L("实时查看容器日志的轻量网页", "即時查看容器日誌的輕量網頁", "Lightweight live viewer for container logs"),
    "tools", "8", "https://dozzle.dev", """services:
  dozzle:
    image: amir20/dozzle:v8.12.0
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
""", mem=32, disk=30, fields=[PORT("HTTP_PORT", 8183)],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}], **web("dozzle", 8080))

app("uptime-kuma", L("Uptime Kuma", "Uptime Kuma", "Uptime Kuma"),
    L("网站和服务的可用性监控，带状态页", "網站與服務的可用性監控，附狀態頁", "Uptime monitoring with status pages"),
    "monitor", "1.23", "https://uptime.kuma.pet", """services:
  uptime-kuma:
    image: louislam/uptime-kuma:1.23.16
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/app/data
    ports: ["127.0.0.1:${HTTP_PORT}:3001"]
""", mem=128, disk=200, fields=[PORT("HTTP_PORT", 3001)],
    ports=[{"name": "web", "port": 3001, "field": "HTTP_PORT", "http": True}], **web("uptime-kuma", 3001))

app("portainer", L("Portainer", "Portainer", "Portainer"),
    L("Docker 的图形化管理界面（与 Berth 功能有重叠）", "Docker 的圖形化管理介面（與 Berth 功能有重疊）", "Docker management UI (overlaps with Berth)"),
    "tools", "2.27", "https://www.portainer.io", """services:
  portainer:
    image: portainer/portainer-ce:2.27.3
    restart: unless-stopped
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ${BERTH_DATA}/data:/data
    ports: ["127.0.0.1:${HTTP_PORT}:9000"]
""", mem=96, disk=100, fields=[PORT("HTTP_PORT", 9000)],
    ports=[{"name": "web", "port": 9000, "field": "HTTP_PORT", "http": True}], **web("portainer", 9000))

# ---------------------------------------------------------------- files
app("filebrowser", L("File Browser", "File Browser", "File Browser"),
    L("网页文件管理器，适合分享一个目录", "網頁檔案管理器，適合分享一個目錄", "Web file manager for sharing a folder"),
    "files", "2.32", "https://filebrowser.org", """services:
  filebrowser:
    image: filebrowser/filebrowser:v2.32.0
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/files:/srv
      - ${BERTH_DATA}/config:/config
      - ${BERTH_DATA}/database:/database
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
""", mem=48, disk=50, fields=[PORT("HTTP_PORT", 8184)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}], **web("filebrowser", 80))

app("syncthing", L("Syncthing", "Syncthing", "Syncthing"),
    L("在多台设备之间同步文件，无需云端", "在多台裝置之間同步檔案，無需雲端", "Sync files between devices without a cloud"),
    "files", "1.29", "https://syncthing.net", """services:
  syncthing:
    image: syncthing/syncthing:1.29
    restart: unless-stopped
    environment: { PUID: "${PUID}", PGID: "${PGID}" }
    volumes:
      - ${BERTH_DATA}/data:/var/syncthing
    ports:
      - "127.0.0.1:${HTTP_PORT}:8384"
      - "${SYNC_PORT}:22000/tcp"
      - "${SYNC_PORT}:22000/udp"
""", mem=128, disk=100,
    fields=[PORT("HTTP_PORT", 8384), PUBPORT("SYNC_PORT", 22000, "同步端口", "同步連接埠", "Sync port")],
    ports=[{"name": "web", "port": 8384, "field": "HTTP_PORT", "http": True}, {"name": "sync", "port": 22000, "field": "SYNC_PORT"}], **web("syncthing", 8384))

app("minio", L("MinIO", "MinIO", "MinIO"),
    L("兼容 S3 的对象存储，也可作备份目的地", "相容 S3 的物件儲存，也可作備份目的地", "S3-compatible object storage, also a backup target"),
    "files", "2025", "https://min.io", """services:
  minio:
    image: minio/minio:RELEASE.2025-04-22T22-12-26Z
    restart: unless-stopped
    command: ["server", "/data", "--console-address", ":9001"]
    environment:
      MINIO_ROOT_USER: ${ADMIN_USER}
      MINIO_ROOT_PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/data
    ports:
      - "127.0.0.1:${API_PORT}:9000"
      - "127.0.0.1:${HTTP_PORT}:9001"
""", mem=192, disk=200,
    fields=[TEXT("ADMIN_USER", "admin", "管理员用户名", "管理員使用者名稱", "Admin user"), PASS(), PORT("API_PORT", 9002, "S3 接口端口", "S3 介面連接埠", "S3 API port"), PORT("HTTP_PORT", 9003, "控制台端口", "主控台連接埠", "Console port")],
    ports=[{"name": "api", "port": 9000, "field": "API_PORT", "http": True}, {"name": "console", "port": 9001, "field": "HTTP_PORT", "http": True}],
    health={"service": "minio", "port": 9001, "path": "/"}, site={"service": "minio", "port": 9001})

app("nextcloud", L("Nextcloud", "Nextcloud", "Nextcloud"),
    L("自建网盘：文件、日历、通讯录、在线文档", "自建網盤：檔案、行事曆、通訊錄、線上文件", "Self-hosted cloud: files, calendar, contacts, documents"),
    "files", "31", "https://nextcloud.com", """services:
  nextcloud:
    image: nextcloud:31-apache
    restart: unless-stopped
    environment:
      SQLITE_DATABASE: nextcloud
      NEXTCLOUD_ADMIN_USER: ${ADMIN_USER}
      NEXTCLOUD_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      NEXTCLOUD_TRUSTED_DOMAINS: ${TRUSTED_DOMAINS}
    volumes:
      - ${BERTH_DATA}/html:/var/www/html
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
""", mem=512, disk=1000,
    fields=[TEXT("ADMIN_USER", "admin", "管理员用户名", "管理員使用者名稱", "Admin user"), PASS(),
            TEXT("TRUSTED_DOMAINS", "localhost", "访问域名", "存取網域", "Domains used to open it"), PORT("HTTP_PORT", 8185)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}],
    health={"service": "nextcloud", "port": 80, "path": "/status.php"}, site={"service": "nextcloud", "port": 80})

# ---------------------------------------------------------------- media
app("jellyfin", L("Jellyfin", "Jellyfin", "Jellyfin"),
    L("家庭影音服务器：电影、剧集、音乐", "家庭影音伺服器：電影、影集、音樂", "Home media server for films, shows and music"),
    "media", "10.10", "https://jellyfin.org", """services:
  jellyfin:
    image: jellyfin/jellyfin:10.10.7
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/config:/config
      - ${BERTH_DATA}/cache:/cache
      - ${BERTH_DATA}/media:/media
    ports: ["127.0.0.1:${HTTP_PORT}:8096"]
""", mem=512, disk=500, fields=[PORT("HTTP_PORT", 8096)],
    ports=[{"name": "web", "port": 8096, "field": "HTTP_PORT", "http": True}],
    health={"service": "jellyfin", "port": 8096, "path": "/health"}, site={"service": "jellyfin", "port": 8096})

app("navidrome", L("Navidrome", "Navidrome", "Navidrome"),
    L("个人音乐流媒体服务器，兼容 Subsonic 客户端", "個人音樂串流伺服器，相容 Subsonic 用戶端", "Personal music streaming, Subsonic compatible"),
    "media", "0.55", "https://www.navidrome.org", """services:
  navidrome:
    image: deluan/navidrome:0.55.2
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/data
      - ${BERTH_DATA}/music:/music:ro
    ports: ["127.0.0.1:${HTTP_PORT}:4533"]
""", mem=96, disk=100, fields=[PORT("HTTP_PORT", 4533)],
    ports=[{"name": "web", "port": 4533, "field": "HTTP_PORT", "http": True}], **web("navidrome", 4533))

app("audiobookshelf", L("Audiobookshelf", "Audiobookshelf", "Audiobookshelf"),
    L("有声书和播客服务器", "有聲書與播客伺服器", "Audiobook and podcast server"),
    "media", "2.21", "https://www.audiobookshelf.org", """services:
  audiobookshelf:
    image: ghcr.io/advplyr/audiobookshelf:2.21.0
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/config:/config
      - ${BERTH_DATA}/metadata:/metadata
      - ${BERTH_DATA}/audiobooks:/audiobooks
      - ${BERTH_DATA}/podcasts:/podcasts
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
""", mem=128, disk=200, fields=[PORT("HTTP_PORT", 8186)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}], **web("audiobookshelf", 80))

app("photoprism", L("PhotoPrism", "PhotoPrism", "PhotoPrism"),
    L("AI 整理的私人相册", "AI 整理的私人相簿", "Private photo library organised with AI"),
    "media", "2025", "https://www.photoprism.app", """services:
  photoprism:
    image: photoprism/photoprism:250426
    restart: unless-stopped
    environment:
      PHOTOPRISM_ADMIN_USER: admin
      PHOTOPRISM_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      PHOTOPRISM_DATABASE_DRIVER: sqlite
      PHOTOPRISM_SITE_URL: http://localhost:${HTTP_PORT}/
    volumes:
      - ${BERTH_DATA}/storage:/photoprism/storage
      - ${BERTH_DATA}/originals:/photoprism/originals
    ports: ["127.0.0.1:${HTTP_PORT}:2342"]
""", mem=1024, disk=2000, fields=[PASS(), PORT("HTTP_PORT", 2342)],
    ports=[{"name": "web", "port": 2342, "field": "HTTP_PORT", "http": True}], **web("photoprism", 2342))

# ---------------------------------------------------------------- security
app("vaultwarden", L("Vaultwarden", "Vaultwarden", "Vaultwarden"),
    L("兼容 Bitwarden 的密码库", "相容 Bitwarden 的密碼庫", "Bitwarden-compatible password vault"),
    "security", "1.33", "https://github.com/dani-garcia/vaultwarden", """services:
  vaultwarden:
    image: vaultwarden/server:1.33.2
    restart: unless-stopped
    environment:
      SIGNUPS_ALLOWED: "${SIGNUPS}"
      ADMIN_TOKEN: ${ADMIN_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/data
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
""", mem=64, disk=100,
    fields=[{"key": "SIGNUPS", "type": "select", "options": ["true", "false"], "default": "true", "label": L("允许注册", "允許註冊", "Allow sign-ups"),
             "hint": L("建好自己的账号后可改为 false", "建好自己的帳號後可改為 false", "Set to false once your accounts exist")},
            PASS("ADMIN_PASSWORD", "管理后台令牌", "管理後台權杖", "Admin page token"), PORT("HTTP_PORT", 8187)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}],
    health={"service": "vaultwarden", "port": 80, "path": "/alive"}, site={"service": "vaultwarden", "port": 80})

app("keycloak", L("Keycloak", "Keycloak", "Keycloak"),
    L("单点登录与身份管理（OIDC、SAML）", "單一登入與身分管理（OIDC、SAML）", "Single sign-on and identity (OIDC, SAML)"),
    "security", "26", "https://www.keycloak.org", """services:
  keycloak:
    image: quay.io/keycloak/keycloak:26.2
    restart: unless-stopped
    command: ["start-dev"]
    environment:
      KC_BOOTSTRAP_ADMIN_USERNAME: admin
      KC_BOOTSTRAP_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      KC_PROXY_HEADERS: xforwarded
    volumes:
      - ${BERTH_DATA}/data:/opt/keycloak/data
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
""", mem=768, disk=500, fields=[PASS(), PORT("HTTP_PORT", 8188)],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}], **web("keycloak", 8080))

# ---------------------------------------------------------------- dev
app("gitea", L("Gitea", "Gitea", "Gitea"),
    L("轻量的自建 Git 服务", "輕量的自建 Git 服務", "Lightweight self-hosted Git service"),
    "dev", "1.23", "https://gitea.com", """services:
  gitea:
    image: gitea/gitea:1.23
    restart: unless-stopped
    environment:
      USER_UID: "${PUID}"
      USER_GID: "${PGID}"
      GITEA__database__DB_TYPE: sqlite3
    volumes:
      - ${BERTH_DATA}/data:/data
      - /etc/localtime:/etc/localtime:ro
    ports:
      - "127.0.0.1:${HTTP_PORT}:3000"
      - "${SSH_PORT}:22"
""", mem=192, disk=300,
    fields=[PORT("HTTP_PORT", 3002), PUBPORT("SSH_PORT", 2222, "Git SSH 端口", "Git SSH 連接埠", "Git SSH port")],
    ports=[{"name": "web", "port": 3000, "field": "HTTP_PORT", "http": True}, {"name": "ssh", "port": 22, "field": "SSH_PORT"}], **web("gitea", 3000))

app("forgejo", L("Forgejo", "Forgejo", "Forgejo"),
    L("社区维护的 Gitea 分支", "社群維護的 Gitea 分支", "Community fork of Gitea"),
    "dev", "11", "https://forgejo.org", """services:
  forgejo:
    image: codeberg.org/forgejo/forgejo:11
    restart: unless-stopped
    environment:
      USER_UID: "${PUID}"
      USER_GID: "${PGID}"
      FORGEJO__database__DB_TYPE: sqlite3
    volumes:
      - ${BERTH_DATA}/data:/data
      - /etc/localtime:/etc/localtime:ro
    ports:
      - "127.0.0.1:${HTTP_PORT}:3000"
      - "${SSH_PORT}:22"
""", mem=192, disk=300,
    fields=[PORT("HTTP_PORT", 3003), PUBPORT("SSH_PORT", 2223, "Git SSH 端口", "Git SSH 連接埠", "Git SSH port")],
    ports=[{"name": "web", "port": 3000, "field": "HTTP_PORT", "http": True}, {"name": "ssh", "port": 22, "field": "SSH_PORT"}], **web("forgejo", 3000))

app("code-server", L("code-server", "code-server", "code-server"),
    L("在浏览器里用 VS Code", "在瀏覽器裡用 VS Code", "VS Code in the browser"),
    "dev", "4.99", "https://coder.com/docs/code-server", """services:
  code-server:
    image: lscr.io/linuxserver/code-server:4.99.4
    restart: unless-stopped
    environment:
      PUID: "${PUID}"
      PGID: "${PGID}"
      TZ: ${TZ}
      PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ${BERTH_DATA}/config:/config
    ports: ["127.0.0.1:${HTTP_PORT}:8443"]
""", mem=512, disk=800, fields=[PASS("ADMIN_PASSWORD", "登录密码", "登入密碼", "Password"), PORT("HTTP_PORT", 8443)],
    ports=[{"name": "web", "port": 8443, "field": "HTTP_PORT", "http": True}],
    health={"service": "code-server", "port": 8443, "path": "/healthz"}, site={"service": "code-server", "port": 8443})

# ---------------------------------------------------------------- productivity
app("n8n", L("n8n", "n8n", "n8n"),
    L("可视化工作流自动化，连接几百种服务", "視覺化工作流程自動化，連接數百種服務", "Visual workflow automation connecting hundreds of services"),
    "productivity", "1.95", "https://n8n.io", """services:
  n8n:
    image: n8nio/n8n:1.95.3
    restart: unless-stopped
    environment:
      N8N_SECURE_COOKIE: "false"
      GENERIC_TIMEZONE: ${TZ}
    volumes:
      - ${BERTH_DATA}/data:/home/node/.n8n
    ports: ["127.0.0.1:${HTTP_PORT}:5678"]
""", mem=384, disk=400, fields=[PORT("HTTP_PORT", 5678)],
    ports=[{"name": "web", "port": 5678, "field": "HTTP_PORT", "http": True}],
    health={"service": "n8n", "port": 5678, "path": "/healthz"}, site={"service": "n8n", "port": 5678})

app("node-red", L("Node-RED", "Node-RED", "Node-RED"),
    L("拖拽连线的物联网和自动化编程工具", "拖拉連線的物聯網與自動化程式工具", "Flow-based programming for IoT and automation"),
    "productivity", "4.0", "https://nodered.org", """services:
  node-red:
    image: nodered/node-red:4.0
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/data
    ports: ["127.0.0.1:${HTTP_PORT}:1880"]
""", mem=192, disk=300, fields=[PORT("HTTP_PORT", 1880)],
    ports=[{"name": "web", "port": 1880, "field": "HTTP_PORT", "http": True}], **web("node-red", 1880))

app("home-assistant", L("Home Assistant", "Home Assistant", "Home Assistant"),
    L("智能家居中枢", "智慧家庭中樞", "Smart-home hub"),
    "productivity", "2025.5", "https://www.home-assistant.io", """services:
  home-assistant:
    image: ghcr.io/home-assistant/home-assistant:2025.5
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/config:/config
      - /etc/localtime:/etc/localtime:ro
    ports: ["127.0.0.1:${HTTP_PORT}:8123"]
""", mem=512, disk=800, fields=[PORT("HTTP_PORT", 8123)],
    ports=[{"name": "web", "port": 8123, "field": "HTTP_PORT", "http": True}], **web("home-assistant", 8123))

app("stirling-pdf", L("Stirling PDF", "Stirling PDF", "Stirling PDF"),
    L("PDF 工具箱：合并、拆分、压缩、OCR、转换", "PDF 工具箱：合併、分割、壓縮、OCR、轉換", "PDF toolbox: merge, split, compress, OCR, convert"),
    "productivity", "0.46", "https://stirlingpdf.com", """services:
  stirling-pdf:
    image: stirlingtools/stirling-pdf:0.46.2
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/configs:/configs
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
""", mem=768, disk=1500, fields=[PORT("HTTP_PORT", 8189)],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}], **web("stirling-pdf", 8080))

app("memos", L("Memos", "Memos", "Memos"),
    L("轻量的私人笔记和备忘录", "輕量的私人筆記與備忘錄", "Lightweight private notes"),
    "productivity", "0.24", "https://www.usememos.com", """services:
  memos:
    image: neosmemo/memos:0.24.3
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/var/opt/memos
    ports: ["127.0.0.1:${HTTP_PORT}:5230"]
""", mem=64, disk=100, fields=[PORT("HTTP_PORT", 5230)],
    ports=[{"name": "web", "port": 5230, "field": "HTTP_PORT", "http": True}], **web("memos", 5230))

app("trilium", L("TriliumNext", "TriliumNext", "TriliumNext"),
    L("层级式个人知识库", "階層式個人知識庫", "Hierarchical personal knowledge base"),
    "productivity", "0.93", "https://github.com/TriliumNext/Notes", """services:
  trilium:
    image: triliumnext/notes:v0.93.0
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/home/node/trilium-data
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
""", mem=192, disk=200, fields=[PORT("HTTP_PORT", 8190)],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}], **web("trilium", 8080))

app("wikijs", L("Wiki.js", "Wiki.js", "Wiki.js"),
    L("现代化的 Wiki 系统", "現代化的 Wiki 系統", "Modern wiki"),
    "productivity", "2.5", "https://js.wiki", """services:
  wikijs:
    image: ghcr.io/requarks/wiki:2.5
    restart: unless-stopped
    environment:
      DB_TYPE: sqlite
      DB_FILEPATH: /wiki/data/db.sqlite
    volumes:
      - ${BERTH_DATA}/data:/wiki/data
    ports: ["127.0.0.1:${HTTP_PORT}:3000"]
""", mem=256, disk=300, fields=[PORT("HTTP_PORT", 3004)],
    ports=[{"name": "web", "port": 3000, "field": "HTTP_PORT", "http": True}], **web("wikijs", 3000))

app("paperless-ngx", L("Paperless-ngx", "Paperless-ngx", "Paperless-ngx"),
    L("扫描件和文档归档，带 OCR 全文搜索", "掃描件與文件歸檔，附 OCR 全文搜尋", "Document archive with OCR and full-text search"),
    "productivity", "2.15", "https://docs.paperless-ngx.com", """services:
  paperless:
    image: ghcr.io/paperless-ngx/paperless-ngx:2.15
    restart: unless-stopped
    depends_on: [redis]
    environment:
      PAPERLESS_REDIS: redis://redis:6379
      PAPERLESS_ADMIN_USER: admin
      PAPERLESS_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
      PAPERLESS_OCR_LANGUAGE: ${OCR_LANG}
      PAPERLESS_OCR_LANGUAGES: "chi-sim chi-tra"
    volumes:
      - ${BERTH_DATA}/data:/usr/src/paperless/data
      - ${BERTH_DATA}/media:/usr/src/paperless/media
      - ${BERTH_DATA}/consume:/usr/src/paperless/consume
    ports: ["127.0.0.1:${HTTP_PORT}:8000"]
  redis:
    image: redis:7.4-alpine
    restart: unless-stopped
""", mem=768, disk=1500,
    fields=[PASS(), {"key": "OCR_LANG", "type": "select", "options": ["chi_sim", "chi_tra", "eng", "chi_sim+eng", "chi_tra+eng"], "default": "chi_tra+eng",
                     "label": L("OCR 语言", "OCR 語言", "OCR language")}, PORT("HTTP_PORT", 8191)],
    ports=[{"name": "web", "port": 8000, "field": "HTTP_PORT", "http": True}], **web("paperless", 8000))

app("bookstack", L("BookStack", "BookStack", "BookStack"),
    L("书架式的团队知识库", "書架式的團隊知識庫", "Bookshelf-style team wiki"),
    "productivity", "25", "https://www.bookstackapp.com", """services:
  bookstack:
    image: lscr.io/linuxserver/bookstack:version-v25.05
    restart: unless-stopped
    depends_on: [db]
    environment:
      PUID: "${PUID}"
      PGID: "${PGID}"
      APP_URL: ${APP_URL}
      APP_KEY: ${APP_KEY}
      DB_HOST: db
      DB_PORT: "3306"
      DB_USERNAME: bookstack
      DB_PASSWORD: ${DB_PASSWORD}
      DB_DATABASE: bookstack
    volumes:
      - ${BERTH_DATA}/config:/config
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
  db:
    image: mariadb:11.4
    restart: unless-stopped
    environment:
      MARIADB_RANDOM_ROOT_PASSWORD: "1"
      MARIADB_DATABASE: bookstack
      MARIADB_USER: bookstack
      MARIADB_PASSWORD: ${DB_PASSWORD}
    volumes:
      - ${BERTH_DATA}/db:/var/lib/mysql
""", mem=512, disk=800,
    fields=[TEXT("APP_URL", "http://localhost:8192", "访问地址", "存取位址", "URL people open"), PASS("DB_PASSWORD", "数据库密码", "資料庫密碼", "Database password"),
            {"key": "APP_KEY", "type": "password", "generate": True, "length": 32, "label": L("应用密钥", "應用金鑰", "App key")}, PORT("HTTP_PORT", 8192)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}], **web("bookstack", 80))

# ---------------------------------------------------------------- monitor
app("grafana", L("Grafana", "Grafana", "Grafana"),
    L("指标可视化仪表盘", "指標視覺化儀表板", "Dashboards for metrics"),
    "monitor", "12", "https://grafana.com", """services:
  grafana:
    image: grafana/grafana:12.0.0
    restart: unless-stopped
    user: "0"
    environment:
      GF_SECURITY_ADMIN_PASSWORD: ${ADMIN_PASSWORD}
    volumes:
      - ${BERTH_DATA}/data:/var/lib/grafana
    ports: ["127.0.0.1:${HTTP_PORT}:3000"]
""", mem=192, disk=200, fields=[PASS(), PORT("HTTP_PORT", 3005)],
    ports=[{"name": "web", "port": 3000, "field": "HTTP_PORT", "http": True}],
    health={"service": "grafana", "port": 3000, "path": "/api/health"}, site={"service": "grafana", "port": 3000})

app("prometheus", L("Prometheus", "Prometheus", "Prometheus"),
    L("时序指标采集与告警", "時序指標蒐集與告警", "Time-series metrics and alerting"),
    "monitor", "3.4", "https://prometheus.io", """services:
  prometheus:
    image: prom/prometheus:v3.4.0
    restart: unless-stopped
    user: "0"
    volumes:
      - ${BERTH_DATA}/data:/prometheus
    ports: ["127.0.0.1:${HTTP_PORT}:9090"]
""", mem=256, disk=1000, fields=[PORT("HTTP_PORT", 9090)],
    ports=[{"name": "web", "port": 9090, "field": "HTTP_PORT", "http": True}],
    health={"service": "prometheus", "port": 9090, "path": "/-/healthy"}, site={"service": "prometheus", "port": 9090})

app("glances", L("Glances", "Glances", "Glances"),
    L("一页看全系统和容器的资源", "一頁看全系統與容器的資源", "System and container resources on one page"),
    "monitor", "4.3", "https://nicolargo.github.io/glances/", """services:
  glances:
    image: nicolargo/glances:4.3.1-full
    restart: unless-stopped
    pid: host
    environment:
      GLANCES_OPT: "-w"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - /etc/os-release:/etc/os-release:ro
    ports: ["127.0.0.1:${HTTP_PORT}:61208"]
""", mem=96, disk=300, fields=[PORT("HTTP_PORT", 61208)],
    ports=[{"name": "web", "port": 61208, "field": "HTTP_PORT", "http": True}], **web("glances", 61208))

# ---------------------------------------------------------------- network
app("qbittorrent", L("qBittorrent", "qBittorrent", "qBittorrent"),
    L("BT 下载，网页管理", "BT 下載，網頁管理", "BitTorrent client with a web UI"),
    "network", "5.1", "https://www.qbittorrent.org", """services:
  qbittorrent:
    image: lscr.io/linuxserver/qbittorrent:5.1.0
    restart: unless-stopped
    environment:
      PUID: "${PUID}"
      PGID: "${PGID}"
      TZ: ${TZ}
      WEBUI_PORT: "8080"
    volumes:
      - ${BERTH_DATA}/config:/config
      - ${BERTH_DATA}/downloads:/downloads
    ports:
      - "127.0.0.1:${HTTP_PORT}:8080"
      - "${PEER_PORT}:6881"
      - "${PEER_PORT}:6881/udp"
""", mem=256, disk=200,
    fields=[PORT("HTTP_PORT", 8193), PUBPORT("PEER_PORT", 6881, "BT 端口", "BT 連接埠", "Peer port")],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}, {"name": "peer", "port": 6881, "field": "PEER_PORT"}], **web("qbittorrent", 8080))

app("transmission", L("Transmission", "Transmission", "Transmission"),
    L("轻量 BT 下载", "輕量 BT 下載", "Lightweight BitTorrent client"),
    "network", "4.0", "https://transmissionbt.com", """services:
  transmission:
    image: lscr.io/linuxserver/transmission:4.0.6
    restart: unless-stopped
    environment:
      PUID: "${PUID}"
      PGID: "${PGID}"
      TZ: ${TZ}
      USER: admin
      PASS: ${ADMIN_PASSWORD}
    volumes:
      - ${BERTH_DATA}/config:/config
      - ${BERTH_DATA}/downloads:/downloads
    ports:
      - "127.0.0.1:${HTTP_PORT}:9091"
      - "${PEER_PORT}:51413"
      - "${PEER_PORT}:51413/udp"
""", mem=96, disk=100,
    fields=[PASS(), PORT("HTTP_PORT", 9091), PUBPORT("PEER_PORT", 51413, "BT 端口", "BT 連接埠", "Peer port")],
    ports=[{"name": "web", "port": 9091, "field": "HTTP_PORT", "http": True}, {"name": "peer", "port": 51413, "field": "PEER_PORT"}], **web("transmission", 9091))

app("adguard-home", L("AdGuard Home", "AdGuard Home", "AdGuard Home"),
    L("全网去广告和隐私保护的 DNS 服务器", "全網去廣告與隱私保護的 DNS 伺服器", "Network-wide ad-blocking DNS server"),
    "network", "0.107", "https://adguard.com/adguard-home.html", """services:
  adguard:
    image: adguard/adguardhome:v0.107.61
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/work:/opt/adguardhome/work
      - ${BERTH_DATA}/conf:/opt/adguardhome/conf
    ports:
      - "127.0.0.1:${HTTP_PORT}:3000"
      - "${DNS_PORT}:53/tcp"
      - "${DNS_PORT}:53/udp"
""", mem=96, disk=200,
    fields=[PORT("HTTP_PORT", 3006), PUBPORT("DNS_PORT", 53, "DNS 端口", "DNS 連接埠", "DNS port")],
    ports=[{"name": "web", "port": 3000, "field": "HTTP_PORT", "http": True}, {"name": "dns", "port": 53, "field": "DNS_PORT"}], **web("adguard", 3000))

app("wg-easy", L("WireGuard Easy", "WireGuard Easy", "WireGuard Easy"),
    L("带网页管理的 WireGuard VPN", "附網頁管理的 WireGuard VPN", "WireGuard VPN with a web UI"),
    "network", "14", "https://github.com/wg-easy/wg-easy", """services:
  wg-easy:
    image: ghcr.io/wg-easy/wg-easy:14
    restart: unless-stopped
    environment:
      WG_HOST: ${WG_HOST}
      PASSWORD_HASH: ""
      PORT: "51821"
      WG_PORT: "${WG_PORT}"
    volumes:
      - ${BERTH_DATA}/data:/etc/wireguard
    ports:
      - "${WG_PORT}:51820/udp"
      - "127.0.0.1:${HTTP_PORT}:51821"
    cap_add: [NET_ADMIN, SYS_MODULE]
    sysctls:
      net.ipv4.ip_forward: "1"
      net.ipv4.conf.all.src_valid_mark: "1"
""", mem=64, disk=50,
    fields=[TEXT("WG_HOST", "", "公网地址或域名", "公網位址或網域", "Public address or domain"), PUBPORT("WG_PORT", 51820, "VPN 端口（UDP）", "VPN 連接埠（UDP）", "VPN port (UDP)"), PORT("HTTP_PORT", 51821)],
    ports=[{"name": "web", "port": 51821, "field": "HTTP_PORT", "http": True}, {"name": "vpn", "port": 51820, "field": "WG_PORT", "proto": "udp"}], **web("wg-easy", 51821))

app("rustdesk-server", L("RustDesk 中继", "RustDesk 中繼", "RustDesk relay"),
    L("自建 RustDesk 远程桌面的 ID 和中继服务器", "自建 RustDesk 遠端桌面的 ID 與中繼伺服器", "Self-hosted ID and relay server for RustDesk remote desktop"),
    "network", "1.1", "https://rustdesk.com", """services:
  hbbs:
    image: rustdesk/rustdesk-server:1.1.14
    restart: unless-stopped
    command: ["hbbs"]
    volumes:
      - ${BERTH_DATA}/data:/root
    ports:
      - "${NAT_PORT}:21115"
      - "${ID_PORT}:21116"
      - "${ID_PORT}:21116/udp"
    depends_on: [hbbr]
  hbbr:
    image: rustdesk/rustdesk-server:1.1.14
    restart: unless-stopped
    command: ["hbbr"]
    volumes:
      - ${BERTH_DATA}/data:/root
    ports:
      - "${RELAY_PORT}:21117"
""", mem=64, disk=50,
    fields=[PUBPORT("ID_PORT", 21116, "ID 端口", "ID 連接埠", "ID port"), PUBPORT("RELAY_PORT", 21117, "中继端口", "中繼連接埠", "Relay port"),
            PUBPORT("NAT_PORT", 21115, "NAT 检测端口", "NAT 偵測連接埠", "NAT test port")],
    ports=[{"name": "id", "port": 21116, "field": "ID_PORT", "public": True}, {"name": "id-udp", "port": 21116, "field": "ID_PORT", "proto": "udp", "public": True},
           {"name": "relay", "port": 21117, "field": "RELAY_PORT", "public": True}, {"name": "nat", "port": 21115, "field": "NAT_PORT", "public": True}])

# ---------------------------------------------------------------- chat
app("ntfy", L("ntfy", "ntfy", "ntfy"),
    L("简单的推送通知服务，也可作 Berth 的告警渠道", "簡單的推播通知服務，也可作 Berth 的告警管道", "Simple push notifications; also a Berth alert channel"),
    "chat", "2.11", "https://ntfy.sh", """services:
  ntfy:
    image: binwiederhier/ntfy:v2.11.0
    restart: unless-stopped
    command: ["serve"]
    volumes:
      - ${BERTH_DATA}/cache:/var/cache/ntfy
      - ${BERTH_DATA}/etc:/etc/ntfy
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
""", mem=32, disk=50, fields=[PORT("HTTP_PORT", 8194)],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}],
    health={"service": "ntfy", "port": 80, "path": "/v1/health"}, site={"service": "ntfy", "port": 80})

app("mattermost", L("Mattermost", "Mattermost", "Mattermost"),
    L("开源团队聊天，类似 Slack", "開源團隊聊天，類似 Slack", "Open-source team chat, Slack-like"),
    "chat", "10.8", "https://mattermost.com", """services:
  mattermost:
    image: mattermost/mattermost-team-edition:10.8
    restart: unless-stopped
    depends_on: [db]
    environment:
      MM_SQLSETTINGS_DRIVERNAME: postgres
      MM_SQLSETTINGS_DATASOURCE: postgres://mattermost:${DB_PASSWORD}@db:5432/mattermost?sslmode=disable
      MM_SERVICESETTINGS_SITEURL: ${SITE_URL}
    volumes:
      - ${BERTH_DATA}/config:/mattermost/config
      - ${BERTH_DATA}/data:/mattermost/data
      - ${BERTH_DATA}/logs:/mattermost/logs
      - ${BERTH_DATA}/plugins:/mattermost/plugins
    ports: ["127.0.0.1:${HTTP_PORT}:8065"]
  db:
    image: postgres:17-alpine
    restart: unless-stopped
    environment:
      POSTGRES_USER: mattermost
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_DB: mattermost
    volumes:
      - ${BERTH_DATA}/db:/var/lib/postgresql/data
""", mem=768, disk=1000,
    fields=[TEXT("SITE_URL", "http://localhost:8065", "访问地址", "存取位址", "URL people open"), PASS("DB_PASSWORD", "数据库密码", "資料庫密碼", "Database password"), PORT("HTTP_PORT", 8065)],
    ports=[{"name": "web", "port": 8065, "field": "HTTP_PORT", "http": True}], **web("mattermost", 8065),
    arch=("amd64",))  # upstream publishes no arm64 image; Berth never emulates x86 on ARM


# ---------------------------------------------------------------- ai
app("ollama", L("Ollama", "Ollama", "Ollama"),
    L("在本机运行开源大模型（Llama、Qwen、DeepSeek 等），提供 API", "在本機執行開源大型模型（Llama、Qwen、DeepSeek 等），提供 API", "Run open models (Llama, Qwen, DeepSeek…) locally behind an API"),
    "ai", "0.12", "https://ollama.com", """services:
  ollama:
    image: ollama/ollama:0.12.6
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/models:/root/.ollama
    ports: ["127.0.0.1:${API_PORT}:11434"]
""", mem=1024, disk=2000, fields=[PORT("API_PORT", 11434, "API 端口", "API 連接埠", "API port")],
    ports=[{"name": "api", "port": 11434, "field": "API_PORT"}],
    health={"service": "ollama", "port": 11434, "path": "/"})

app("open-webui", L("Open WebUI", "Open WebUI", "Open WebUI"),
    L("类似 ChatGPT 的聊天界面，自带 Ollama，在本机跑大模型", "類似 ChatGPT 的聊天介面，內建 Ollama，在本機執行大型模型", "A ChatGPT-style chat UI with Ollama included, running models locally"),
    "ai", "0.6", "https://openwebui.com", """services:
  open-webui:
    image: ghcr.io/open-webui/open-webui:v0.6.34
    restart: unless-stopped
    environment:
      OLLAMA_BASE_URL: http://ollama:11434
      WEBUI_SECRET_KEY: ${SECRET_KEY}
    volumes:
      - ${BERTH_DATA}/webui:/app/backend/data
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
    depends_on: [ollama]
  ollama:
    image: ollama/ollama:0.12.6
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/models:/root/.ollama
""", mem=1536, disk=6000,
    fields=[PORT("HTTP_PORT", 3010), PASS("SECRET_KEY", "会话密钥", "工作階段金鑰", "Session secret")],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}],
    health={"service": "open-webui", "port": 8080, "path": "/health"}, site={"service": "open-webui", "port": 8080})

# ---------------------------------------------------------------- web
app("wordpress", L("WordPress", "WordPress", "WordPress"),
    L("最流行的建站和博客程序（容器版，自带 MariaDB）", "最流行的架站與部落格程式（容器版，內建 MariaDB）", "The most popular site and blog software (container, with MariaDB)"),
    "web", "6.8", "https://wordpress.org", """services:
  wordpress:
    image: wordpress:6.8-php8.3-apache
    restart: unless-stopped
    environment:
      WORDPRESS_DB_HOST: db
      WORDPRESS_DB_USER: wordpress
      WORDPRESS_DB_PASSWORD: ${DB_PASSWORD}
      WORDPRESS_DB_NAME: wordpress
    volumes:
      - ${BERTH_DATA}/html:/var/www/html
    ports: ["127.0.0.1:${HTTP_PORT}:80"]
    depends_on: [db]
  db:
    image: mariadb:11.4
    restart: unless-stopped
    environment:
      MARIADB_DATABASE: wordpress
      MARIADB_USER: wordpress
      MARIADB_PASSWORD: ${DB_PASSWORD}
      MARIADB_RANDOM_ROOT_PASSWORD: "1"
    volumes:
      - ${BERTH_DATA}/db:/var/lib/mysql
""", mem=384, disk=800,
    fields=[PORT("HTTP_PORT", 8195), PASS("DB_PASSWORD", "数据库密码", "資料庫密碼", "Database password")],
    ports=[{"name": "web", "port": 80, "field": "HTTP_PORT", "http": True}],
    health={"service": "wordpress", "port": 80, "path": "/readme.html"}, site={"service": "wordpress", "port": 80})

app("halo", L("Halo", "Halo", "Halo"),
    L("国产开源建站工具，博客、文档、主题和插件丰富", "開源架站工具，部落格、文件、主題與外掛豐富", "Open-source site builder: blogs, docs, themes and plugins"),
    "web", "2.21", "https://www.halo.run", """services:
  halo:
    image: halohub/halo:2.21
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/root/.halo2
    ports: ["127.0.0.1:${HTTP_PORT}:8090"]
""", mem=512, disk=600, fields=[PORT("HTTP_PORT", 8090)],
    ports=[{"name": "web", "port": 8090, "field": "HTTP_PORT", "http": True}],
    health={"service": "halo", "port": 8090, "path": "/actuator/health"}, site={"service": "halo", "port": 8090})

# ---------------------------------------------------------------- files (more)
app("openlist", L("OpenList", "OpenList", "OpenList"),
    L("把各种网盘和存储挂成一个文件列表（AList 的社区延续版）", "把各種網盤與儲存掛成一個檔案列表（AList 的社群延續版）", "One file list over many cloud drives and storages (the community fork of AList)"),
    "files", "4.1", "https://github.com/OpenListTeam/OpenList", """services:
  openlist:
    image: openlistteam/openlist:v4.1.5
    restart: unless-stopped
    user: "0:0"  # since 4.1 it runs as a user that cannot write a bind-mounted data folder
    volumes:
      - ${BERTH_DATA}/data:/opt/openlist/data
    ports: ["127.0.0.1:${HTTP_PORT}:5244"]
""", mem=128, disk=150, fields=[PORT("HTTP_PORT", 5244)],
    ports=[{"name": "web", "port": 5244, "field": "HTTP_PORT", "http": True}], **web("openlist", 5244))

# ---------------------------------------------------------------- phase 9: popular with 宝塔/1Panel users
app("cloudreve", L("Cloudreve", "Cloudreve", "Cloudreve"),
    L("自建网盘：多用户、分享链接、在线预览，可接本机或对象存储", "自建網盤：多使用者、分享連結、線上預覽，可接本機或物件儲存", "Self-hosted cloud drive: users, share links, previews; local or object storage"),
    "files", "4.19", "https://cloudreve.org", """services:
  cloudreve:
    image: cloudreve/cloudreve:4.19.1
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/cloudreve/data
    ports: ["127.0.0.1:${HTTP_PORT}:5212"]
""", mem=192, disk=200, fields=[PORT("HTTP_PORT", 5212)],
    ports=[{"name": "web", "port": 5212, "field": "HTTP_PORT", "http": True}], **web("cloudreve", 5212))

app("frps", L("frp 服务端（内网穿透）", "frp 伺服端（內網穿透）", "frp server (expose services behind NAT)"),
    L("让家里或内网的机器通过这台服务器对外提供服务；客户端用 frpc 连接", "讓家裡或內網的機器透過這台伺服器對外提供服務；用戶端用 frpc 連線", "Lets machines behind NAT publish services through this server; clients connect with frpc"),
    "network", "0.72", "https://github.com/fatedier/frp", """services:
  frps:
    image: snowdreamtech/frps:0.72.0-alpine
    restart: unless-stopped
    entrypoint: ["frps"]  # the image's own entrypoint ignores arguments and wants a config file
    command: ["--bind-port", "7000", "--token", "${FRP_TOKEN}", "--allow-ports", "${PROXY_PORTS}",
              "--dashboard-addr", "0.0.0.0", "--dashboard-port", "7500", "--dashboard-user", "admin", "--dashboard-pwd", "${ADMIN_PASSWORD}"]
    ports:
      - "${BIND_PORT}:7000"
      - "${PROXY_PORTS}:${PROXY_PORTS}"
      - "127.0.0.1:${HTTP_PORT}:7500"
""", mem=32, disk=30,
    fields=[PASS("FRP_TOKEN", "连接令牌（frpc 里填 auth.token）", "連線權杖（frpc 裡填 auth.token）", "Token (auth.token in frpc)"), PASS(),
            PUBPORT("BIND_PORT", 7000, "frpc 连接端口", "frpc 連線連接埠", "Port frpc connects to"),
            TEXT("PROXY_PORTS", "6000-6010", "可分配给客户端的端口范围", "可分配給用戶端的連接埠範圍", "Ports clients may use"),
            PORT("HTTP_PORT", 7500)],
    ports=[{"name": "bind", "port": 7000, "field": "BIND_PORT", "public": True}, {"name": "web", "port": 7500, "field": "HTTP_PORT", "http": True}],
    health={"service": "frps", "port": 7500, "path": "/"}, site={"service": "frps", "port": 7500})

app("qinglong", L("青龙面板", "青龍面板", "Qinglong"),
    L("定时任务管理面板：Python、Node、Shell 脚本，依赖管理、日志和通知", "排程任務管理面板：Python、Node、Shell 腳本，相依管理、日誌與通知", "Scheduled-script panel for Python, Node and shell, with dependencies, logs and notifications"),
    "tools", "2.24", "https://github.com/whyour/qinglong", """services:
  qinglong:
    image: whyour/qinglong:2.24.0
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/ql/data
    ports: ["127.0.0.1:${HTTP_PORT}:5700"]
""", mem=256, disk=600, fields=[PORT("HTTP_PORT", 5700)],
    ports=[{"name": "web", "port": 5700, "field": "HTTP_PORT", "http": True}], **web("qinglong", 5700))

app("lobe-chat", L("LobeChat", "LobeChat", "LobeChat"),
    L("好看的 AI 聊天界面，支持 OpenAI、Claude、DeepSeek、通义、Ollama 等，带访问密码", "好看的 AI 聊天介面，支援 OpenAI、Claude、DeepSeek、通義、Ollama 等，附存取密碼", "A polished AI chat UI for OpenAI, Claude, DeepSeek, Qwen, Ollama and more, behind an access code"),
    "ai", "1.143", "https://lobehub.com", """services:
  lobe-chat:
    image: lobehub/lobe-chat:1.143.3
    restart: unless-stopped
    environment:
      ACCESS_CODE: ${ADMIN_PASSWORD}
    ports: ["127.0.0.1:${HTTP_PORT}:3210"]
""", mem=256, disk=600, fields=[PASS("ADMIN_PASSWORD", "访问密码", "存取密碼", "Access code"), PORT("HTTP_PORT", 3210)],
    ports=[{"name": "web", "port": 3210, "field": "HTTP_PORT", "http": True}], **web("lobe-chat", 3210))

app("sun-panel", L("Sun-Panel", "Sun-Panel", "Sun-Panel"),
    L("服务器和 NAS 的导航页：把常用网址和服务排成一页，内网外网地址自动切换", "伺服器與 NAS 的導覽頁：把常用網址和服務排成一頁，內外網位址自動切換", "A start page for your server or NAS; switches between LAN and public addresses"),
    "productivity", "1.8", "https://sun-panel-doc.enianteam.com", """services:
  sun-panel:
    image: hslr/sun-panel:1.8.1
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/conf:/app/conf
      - ${BERTH_DATA}/uploads:/app/uploads
      - ${BERTH_DATA}/database:/app/database
    ports: ["127.0.0.1:${HTTP_PORT}:3002"]
""", mem=64, disk=60, fields=[PORT("HTTP_PORT", 3012)],
    ports=[{"name": "web", "port": 3002, "field": "HTTP_PORT", "http": True}], **web("sun-panel", 3002))

app("homepage", L("Homepage", "Homepage", "Homepage"),
    L("可高度定制的服务导航页，能显示各服务的状态和数据", "可高度自訂的服務導覽頁，能顯示各服務的狀態與資料", "A highly customisable dashboard showing your services and their status"),
    "productivity", "2.8", "https://gethomepage.dev", """services:
  homepage:
    image: ghcr.io/gethomepage/homepage:v2.8.0
    restart: unless-stopped
    environment:
      HOMEPAGE_ALLOWED_HOSTS: "*"
    volumes:
      - ${BERTH_DATA}/config:/app/config
    ports: ["127.0.0.1:${HTTP_PORT}:3000"]
""", mem=128, disk=300, fields=[PORT("HTTP_PORT", 3013)],
    ports=[{"name": "web", "port": 3000, "field": "HTTP_PORT", "http": True}], **web("homepage", 3000))

app("rustfs", L("RustFS（对象存储）", "RustFS（物件儲存）", "RustFS (object storage)"),
    L("兼容 S3 的对象存储，可替代 MinIO；带网页控制台", "相容 S3 的物件儲存，可替代 MinIO；附網頁主控台", "S3-compatible object storage, a MinIO alternative, with a web console"),
    "files", "1.0", "https://rustfs.com", """services:
  rustfs:
    image: rustfs/rustfs:1.0.5
    restart: unless-stopped
    user: "0:0"  # the image's own user cannot write a bind-mounted data folder
    environment:
      RUSTFS_ACCESS_KEY: admin
      RUSTFS_SECRET_KEY: ${ADMIN_PASSWORD}
      RUSTFS_CONSOLE_ENABLE: "true"
      RUSTFS_VOLUMES: /data
    volumes:
      - ${BERTH_DATA}/data:/data
    ports:
      - "127.0.0.1:${S3_PORT}:9000"
      - "127.0.0.1:${HTTP_PORT}:9001"
""", mem=256, disk=200,
    fields=[PASS("ADMIN_PASSWORD", "admin 的密钥（Secret Key）", "admin 的密鑰（Secret Key）", "Secret key for admin"), PORT("HTTP_PORT", 9011),
            PORT("S3_PORT", 9010, "S3 接口端口", "S3 介面連接埠", "S3 API port")],
    ports=[{"name": "web", "port": 9001, "field": "HTTP_PORT", "http": True}, {"name": "s3", "port": 9000, "field": "S3_PORT", "http": True}],
    health={"service": "rustfs", "port": 9001, "path": "/"}, site={"service": "rustfs", "port": 9001})

# ---------------------------------------------------------------- phase 10
app("immich", L("Immich（相册）", "Immich（相簿）", "Immich (photos)"),
    L("自建 Google 相册：手机自动备份照片视频，人脸识别、地图、按内容搜索", "自建 Google 相簿：手機自動備份照片影片，人臉辨識、地圖、依內容搜尋", "Self-hosted Google Photos: phone backup, faces, maps and search by content"),
    "media", "3.4", "https://immich.app", """services:
  immich-server:
    image: ghcr.io/immich-app/immich-server:v3.4.1
    restart: unless-stopped
    environment:
      DB_HOSTNAME: database
      DB_USERNAME: immich
      DB_PASSWORD: ${DB_PASSWORD}
      DB_DATABASE_NAME: immich
      REDIS_HOSTNAME: redis
    volumes:
      - ${BERTH_DATA}/library:/data
    ports: ["127.0.0.1:${HTTP_PORT}:2283"]
    depends_on: [redis, database]
  immich-machine-learning:
    image: ghcr.io/immich-app/immich-machine-learning:v3.4.1
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/model-cache:/cache
  redis:
    image: valkey/valkey:9-alpine
    restart: unless-stopped
  database:
    image: ghcr.io/immich-app/postgres:14-vectorchord0.4.3-pgvectors0.2.0
    restart: unless-stopped
    environment:
      POSTGRES_PASSWORD: ${DB_PASSWORD}
      POSTGRES_USER: immich
      POSTGRES_DB: immich
      POSTGRES_INITDB_ARGS: "--data-checksums"
    volumes:
      - ${BERTH_DATA}/postgres:/var/lib/postgresql/data
    shm_size: 128mb
""", mem=1536, disk=4000,
    fields=[PORT("HTTP_PORT", 2283), PASS("DB_PASSWORD", "数据库密码", "資料庫密碼", "Database password")],
    ports=[{"name": "web", "port": 2283, "field": "HTTP_PORT", "http": True}],
    health={"service": "immich-server", "port": 2283, "path": "/api/server/ping"}, site={"service": "immich-server", "port": 2283})

app("maxkb", L("MaxKB（AI 知识库）", "MaxKB（AI 知識庫）", "MaxKB (AI knowledge base)"),
    L("基于大模型的知识库问答：上传文档即可做客服、内部问答，可接 DeepSeek、通义、Ollama 等", "以大模型為基礎的知識庫問答：上傳文件即可做客服、內部問答，可接 DeepSeek、通義、Ollama 等", "Knowledge-base Q&A on LLMs: upload documents for support or internal answers; DeepSeek, Qwen, Ollama and more"),
    "ai", "2.10", "https://maxkb.cn", """services:
  maxkb:
    image: 1panel/maxkb:v2.10.8-lts
    restart: unless-stopped
    volumes:
      - ${BERTH_DATA}/data:/opt/maxkb
    ports: ["127.0.0.1:${HTTP_PORT}:8080"]
""", mem=1536, disk=3000, fields=[PORT("HTTP_PORT", 8085)],
    ports=[{"name": "web", "port": 8080, "field": "HTTP_PORT", "http": True}], **web("maxkb", 8080))

# ---------------------------------------------------------------- write
def dump_yaml(d):
    """Top-level keys in block style; values as JSON flow (valid YAML), lists one item a line."""
    out = []
    for k, v in d.items():
        if v is None or v == [] or v == {}:
            continue
        if isinstance(v, list) and v and not all(isinstance(x, str) for x in v):
            out.append(f"{k}:")
            for x in v:
                out.append("  - " + json.dumps(x, ensure_ascii=False))
        else:
            out.append(f"{k}: " + json.dumps(v, ensure_ascii=False))
    return "\n".join(out)

def fmt(v):
    if isinstance(v, bool):
        return "true" if v else "false"
    if isinstance(v, int):
        return str(v)
    return json.dumps(v, ensure_ascii=False)

def existing_verified(path):
    try:
        txt = open(path, encoding="utf-8").read()
    except FileNotFoundError:
        return None
    m = re.search(r"^verified: (\{.*\})$", txt, re.M)
    return m.group(1) if m else None

# Images that run as a non-root user: their data folders are created for that user.
OWNERS = {"n8n": "1000:1000", "node-red": "1000:1000", "wikijs": "1000:1000", "pgadmin": "5050:5050",
          "mattermost": "2000:2000", "keycloak": "1000:0"}
# MinIO stopped publishing community container images (2025); not in the catalog.
DROPPED = {"minio"}

def main():
    keep = set()
    APPS[:] = [a for a in APPS if a["id"] not in DROPPED]
    for a in APPS:
        d = os.path.join(ROOT, a["id"])
        os.makedirs(d, exist_ok=True)
        keep.add(a["id"])
        meta = {k: a[k] for k in ("id", "name", "summary", "category", "version", "website", "arch")}
        meta["verified"] = None
        meta.update({"mem_mb": a["mem_mb"], "disk_mb": a["disk_mb"], "ports": a["ports"], "fields": a["fields"]})
        if a["health"]:
            meta["health"] = a["health"]
        if a["site"]:
            meta["site"] = a["site"]
        if a["id"] in OWNERS:
            meta["data_owner"] = OWNERS[a["id"]]
        body = dump_yaml(meta)
        ver = existing_verified(os.path.join(d, "berth.yaml")) or '{"arm64_16k": "untested"}'
        body = body.replace('arch: [', 'verified: ' + ver + '\narch: [', 1)
        with open(os.path.join(d, "berth.yaml"), "w", encoding="utf-8") as f:
            f.write("# generated by scripts/apps/gen.py — edit there\n" + body + "\n")
        with open(os.path.join(d, "compose.yaml"), "w", encoding="utf-8") as f:
            f.write(a["compose"])
    print(f"{len(APPS)} apps written")

if __name__ == "__main__":
    main()
