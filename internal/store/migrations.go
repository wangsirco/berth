package store

var migrations = []string{
	// v1: 基础
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	 CREATE TABLE secrets (name TEXT PRIMARY KEY, enc BLOB NOT NULL, updated_at INTEGER NOT NULL);
	 CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		username TEXT NOT NULL UNIQUE,
		display_name TEXT NOT NULL,
		password_hash TEXT NOT NULL DEFAULT '',
		role TEXT NOT NULL CHECK (role IN ('super','admin','viewer')),
		perms TEXT NOT NULL DEFAULT '[]',        -- JSON 权限键数组（admin 用）
		scope TEXT NOT NULL DEFAULT '{}',        -- JSON {hosts:[],paths:[]}，空=不限
		login_mode TEXT NOT NULL DEFAULT 'password' CHECK (login_mode IN ('password','password_2fa','passkey_only')),
		locale TEXT NOT NULL DEFAULT '',
		disabled INTEGER NOT NULL DEFAULT 0,
		created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, last_login_at INTEGER NOT NULL DEFAULT 0);
	 CREATE TABLE sessions (
		token_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		csrf TEXT NOT NULL, ip TEXT NOT NULL, user_agent TEXT NOT NULL, method TEXT NOT NULL,
		stage TEXT NOT NULL DEFAULT 'full' CHECK (stage IN ('second_step','full')),
		created_at INTEGER NOT NULL, seen_at INTEGER NOT NULL);
	 CREATE TABLE passkeys (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		rp_id TEXT NOT NULL, name TEXT NOT NULL, cred TEXT NOT NULL, created_at INTEGER NOT NULL, last_used_at INTEGER NOT NULL DEFAULT 0);
	 CREATE TABLE totp (user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, secret_enc BLOB NOT NULL,
		confirmed INTEGER NOT NULL DEFAULT 0, last_step INTEGER NOT NULL DEFAULT 0, recovery TEXT NOT NULL DEFAULT '[]');
	 CREATE TABLE trusted_devices (id TEXT PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		label TEXT NOT NULL, ip TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL);
	 CREATE TABLE setup (code_hash TEXT PRIMARY KEY, expires_at INTEGER NOT NULL)`,
	// v2: 令牌（API 与 Agent）
	`CREATE TABLE tokens (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		kind TEXT NOT NULL CHECK (kind IN ('pat','agt','cli')), name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
		prefix TEXT NOT NULL,                    -- 前 12 位，便于辨认
		scopes TEXT NOT NULL,                    -- JSON ["containers:read","containers:write",...]
		limits TEXT NOT NULL DEFAULT '{}',       -- JSON {hosts:[],paths:[]}
		ip_allow TEXT NOT NULL DEFAULT '[]', danger_policy TEXT NOT NULL DEFAULT 'run' CHECK (danger_policy IN ('run','confirm','readonly')),
		rate_per_min INTEGER NOT NULL DEFAULT 120, note TEXT NOT NULL DEFAULT '',
		not_before INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL DEFAULT 0,
		last_used_at INTEGER NOT NULL DEFAULT 0, last_ip TEXT NOT NULL DEFAULT '', uses INTEGER NOT NULL DEFAULT 0,
		revoked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL)`,
	// v3: 改动（审计即改动记录）、任务、关系
	`CREATE TABLE changes (
		id TEXT PRIMARY KEY, ts INTEGER NOT NULL, host TEXT NOT NULL, kind TEXT NOT NULL,
		actor_type TEXT NOT NULL, actor_id TEXT NOT NULL, actor_name TEXT NOT NULL, via TEXT NOT NULL,
		ip TEXT NOT NULL, ai_session TEXT NOT NULL DEFAULT '', intent TEXT NOT NULL DEFAULT '',
		params TEXT NOT NULL, plan TEXT NOT NULL, risk TEXT NOT NULL, status TEXT NOT NULL,
		result TEXT NOT NULL DEFAULT '', log TEXT NOT NULL DEFAULT '', undo TEXT NOT NULL DEFAULT '',
		reverts TEXT NOT NULL DEFAULT '', reverted_by TEXT NOT NULL DEFAULT '', finished_at INTEGER NOT NULL DEFAULT 0);
	 CREATE INDEX changes_ts ON changes(ts DESC);
	 CREATE INDEX changes_obj ON changes(host, kind);
	 CREATE TABLE tasks (
		id TEXT PRIMARY KEY, host TEXT NOT NULL, title TEXT NOT NULL, change_id TEXT NOT NULL DEFAULT '',
		heavy INTEGER NOT NULL DEFAULT 0, mem_mb INTEGER NOT NULL DEFAULT 0, steps TEXT NOT NULL DEFAULT '[]',
		step INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, wait_reason TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL, created_at INTEGER NOT NULL, started_at INTEGER NOT NULL DEFAULT 0, finished_at INTEGER NOT NULL DEFAULT 0);
	 CREATE TABLE task_logs (task_id TEXT NOT NULL, seq INTEGER NOT NULL, ts INTEGER NOT NULL, line TEXT NOT NULL, PRIMARY KEY(task_id, seq));
	 CREATE TABLE relations (from_type TEXT NOT NULL, from_id TEXT NOT NULL, to_type TEXT NOT NULL, to_id TEXT NOT NULL, kind TEXT NOT NULL,
		host TEXT NOT NULL, source TEXT NOT NULL DEFAULT 'module', PRIMARY KEY(from_type, from_id, to_type, to_id, kind));
	 CREATE INDEX relations_to ON relations(to_type, to_id)`,
	// v4: 指标降采样与面板访问记录
	`CREATE TABLE metrics_min (host TEXT NOT NULL, ts INTEGER NOT NULL, series TEXT NOT NULL, v REAL NOT NULL, PRIMARY KEY(host, series, ts)) WITHOUT ROWID;
	 CREATE TABLE metrics_hour (host TEXT NOT NULL, ts INTEGER NOT NULL, series TEXT NOT NULL, v REAL NOT NULL, PRIMARY KEY(host, series, ts)) WITHOUT ROWID;
	 CREATE TABLE logins (id INTEGER PRIMARY KEY AUTOINCREMENT, ts INTEGER NOT NULL, username TEXT NOT NULL, user_id INTEGER,
		ok INTEGER NOT NULL, method TEXT NOT NULL, ip TEXT NOT NULL, user_agent TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '')`,
	// v5: passkey credential id (for signature-counter updates); session re-auth time
	`ALTER TABLE passkeys ADD COLUMN cred_id TEXT NOT NULL DEFAULT '';
	 CREATE INDEX passkeys_user ON passkeys(user_id);
	 ALTER TABLE sessions ADD COLUMN reauth_at INTEGER NOT NULL DEFAULT 0`,
	// v6: 远程主机（agent 主动连回面板）与一次性加入码
	`CREATE TABLE hosts (name TEXT PRIMARY KEY, key_id TEXT NOT NULL UNIQUE, secret_enc BLOB NOT NULL,
		os TEXT NOT NULL DEFAULT '', arch TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '',
		added_at INTEGER NOT NULL, last_seen INTEGER NOT NULL DEFAULT 0, addr TEXT NOT NULL DEFAULT '');
	 CREATE TABLE join_codes (code_hash TEXT PRIMARY KEY, name TEXT NOT NULL, created_by INTEGER NOT NULL,
		expires_at INTEGER NOT NULL, used_at INTEGER NOT NULL DEFAULT 0)`,
	// v7: 网站（每台主机一个 Caddy 入口）
	`CREATE TABLE sites (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, primary_domain TEXT NOT NULL,
		domains TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, target TEXT NOT NULL, tls TEXT NOT NULL, options TEXT NOT NULL,
		created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
	 CREATE INDEX sites_host ON sites(host)`,
	// v8: 证书中心（自签、内部 CA、导入；ACME 由 Caddy 管理不入库）
	`CREATE TABLE certs (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, source TEXT NOT NULL, info TEXT NOT NULL,
		fields TEXT NOT NULL DEFAULT '', auto_renew INTEGER NOT NULL DEFAULT 0, history TEXT NOT NULL DEFAULT '[]',
		cert_pem TEXT NOT NULL, key_enc BLOB NOT NULL, not_after INTEGER NOT NULL, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`,
	// v9: DNS 账号与 DDNS
	`CREATE TABLE integrations (id TEXT PRIMARY KEY, kind TEXT NOT NULL, name TEXT NOT NULL, secret_enc BLOB NOT NULL,
		meta TEXT NOT NULL DEFAULT '{}', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
	 CREATE TABLE ddns (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, integration TEXT NOT NULL, zone_id TEXT NOT NULL,
		name TEXT NOT NULL, type TEXT NOT NULL, proxied INTEGER NOT NULL DEFAULT 0, enabled INTEGER NOT NULL DEFAULT 1,
		record_id TEXT NOT NULL DEFAULT '', last_ip TEXT NOT NULL DEFAULT '', last_check INTEGER NOT NULL DEFAULT 0,
		last_error TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`,
	// v10: 网站访问统计（分钟保留 2 天，小时保留 90 天并带独立访客寄存器）
	`CREATE TABLE site_stats_min (host TEXT NOT NULL, site TEXT NOT NULL, ts INTEGER NOT NULL, data TEXT NOT NULL, PRIMARY KEY(host, site, ts)) WITHOUT ROWID;
	 CREATE TABLE site_stats_hour (host TEXT NOT NULL, site TEXT NOT NULL, ts INTEGER NOT NULL, data TEXT NOT NULL, PRIMARY KEY(host, site, ts)) WITHOUT ROWID;
	 CREATE TABLE site_stats_ack (host TEXT PRIMARY KEY, boot TEXT NOT NULL, seq INTEGER NOT NULL)`,
	// v11: 内网镜像仓库里由 Berth 推送的标签（保留策略用）
	`CREATE TABLE registry_tags (repo TEXT NOT NULL, tag TEXT NOT NULL, digest TEXT NOT NULL DEFAULT '', pushed_at INTEGER NOT NULL,
		source TEXT NOT NULL DEFAULT '', PRIMARY KEY(repo, tag))`,
	// v12: 流水线与运行记录
	`CREATE TABLE pipelines (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, host TEXT NOT NULL, source TEXT NOT NULL,
		token_enc BLOB, spec TEXT NOT NULL DEFAULT '', cron TEXT NOT NULL DEFAULT '', hook_secret_enc BLOB NOT NULL,
		created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
	 CREATE TABLE pipeline_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, pipeline_id INTEGER NOT NULL, number INTEGER NOT NULL,
		status TEXT NOT NULL, trigger TEXT NOT NULL, actor TEXT NOT NULL DEFAULT '', ref TEXT NOT NULL DEFAULT '',
		commit_sha TEXT NOT NULL DEFAULT '', version TEXT NOT NULL, image TEXT NOT NULL DEFAULT '', digest TEXT NOT NULL DEFAULT '',
		steps TEXT NOT NULL DEFAULT '[]', error TEXT NOT NULL DEFAULT '', change_id TEXT NOT NULL DEFAULT '',
		started_at INTEGER NOT NULL, finished_at INTEGER NOT NULL DEFAULT 0);
	 CREATE UNIQUE INDEX pipeline_runs_num ON pipeline_runs(pipeline_id, number)`,
	// v13: 发布记录；仓库内定义的流水线最近一次读到的定义
	`CREATE TABLE releases (id INTEGER PRIMARY KEY AUTOINCREMENT, pipeline_id INTEGER NOT NULL, run_id INTEGER NOT NULL DEFAULT 0,
		version TEXT NOT NULL DEFAULT '', host TEXT NOT NULL, compose TEXT NOT NULL, service TEXT NOT NULL, image TEXT NOT NULL,
		old_image TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, actor TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '',
		change_id TEXT NOT NULL DEFAULT '', started_at INTEGER NOT NULL, ms INTEGER NOT NULL DEFAULT 0);
	 CREATE INDEX releases_pipeline ON releases(pipeline_id);
	 CREATE TABLE pipeline_specs (pipeline_id INTEGER PRIMARY KEY, spec TEXT NOT NULL)`,
	// v14: 商店安装的应用实例（密码类字段加密保存）
	`CREATE TABLE apps (name TEXT NOT NULL, host TEXT NOT NULL, app_id TEXT NOT NULL, version TEXT NOT NULL,
		vals TEXT NOT NULL DEFAULT '{}', secrets_enc BLOB, runtime TEXT NOT NULL DEFAULT '',
		installed_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, PRIMARY KEY(host, name))`,
	// v15: 登记的数据库实例（管理员密码加密）
	`CREATE TABLE databases (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, name TEXT NOT NULL, engine TEXT NOT NULL,
		container TEXT NOT NULL DEFAULT '', addr TEXT NOT NULL DEFAULT '', admin_user TEXT NOT NULL DEFAULT '', admin_pass_enc BLOB,
		created_at INTEGER NOT NULL, UNIQUE(host, name))`,
	// v16: 备份目的地、任务与运行记录
	`CREATE TABLE backup_targets (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, cfg_enc BLOB NOT NULL, created_at INTEGER NOT NULL);
	 CREATE TABLE backup_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, host TEXT NOT NULL, target_id INTEGER NOT NULL,
		spec TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL);
	 CREATE TABLE backup_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL,
		snapshot TEXT NOT NULL DEFAULT '', bytes INTEGER NOT NULL DEFAULT 0, message TEXT NOT NULL DEFAULT '', started INTEGER NOT NULL,
		finished INTEGER NOT NULL DEFAULT 0, change_id TEXT NOT NULL DEFAULT '');
	 CREATE INDEX backup_runs_job ON backup_runs(job_id)`,
	// v17: 告警规则覆盖、告警事件、个人通知渠道
	`CREATE TABLE alert_rules (key TEXT PRIMARY KEY, spec TEXT NOT NULL);
	 CREATE TABLE alert_events (id INTEGER PRIMARY KEY AUTOINCREMENT, key TEXT NOT NULL, rule TEXT NOT NULL, category TEXT NOT NULL,
		severity TEXT NOT NULL, host TEXT NOT NULL DEFAULT '', params TEXT NOT NULL DEFAULT '{}', state TEXT NOT NULL,
		started INTEGER NOT NULL, resolved_at INTEGER, notified INTEGER NOT NULL DEFAULT 0);
	 CREATE INDEX alert_events_key ON alert_events(key, state);
	 CREATE TABLE notify_channels (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL, kind TEXT NOT NULL, name TEXT NOT NULL,
		cfg_enc BLOB NOT NULL, categories TEXT NOT NULL DEFAULT '[]', last_sent INTEGER NOT NULL DEFAULT 0, last_error TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL)`,
	// v18: 可用性检查、按天汇总、公开状态页
	`CREATE TABLE uptime_checks (id INTEGER PRIMARY KEY AUTOINCREMENT, spec TEXT NOT NULL, created_at INTEGER NOT NULL);
	 CREATE TABLE uptime_results (check_id INTEGER NOT NULL, ts INTEGER NOT NULL, ok INTEGER NOT NULL, ms INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '');
	 CREATE INDEX uptime_results_check ON uptime_results(check_id, ts);
	 CREATE TABLE uptime_daily (check_id INTEGER NOT NULL, day TEXT NOT NULL, up INTEGER NOT NULL, total INTEGER NOT NULL, ms_sum INTEGER NOT NULL,
		PRIMARY KEY(check_id, day));
	 CREATE TABLE status_pages (slug TEXT PRIMARY KEY, spec TEXT NOT NULL)`,
	// v19: 防火墙规则（按主机、有序）
	`CREATE TABLE fw_rules (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, position INTEGER NOT NULL, spec TEXT NOT NULL, created_at INTEGER NOT NULL);
	 CREATE INDEX fw_rules_host ON fw_rules(host, position)`,
	// v20: 封禁、登录失败记录（防爆破）
	`CREATE TABLE bans (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, addr TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
		source TEXT NOT NULL, layers TEXT NOT NULL DEFAULT '["local"]', created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL DEFAULT 0, revoked_at INTEGER NOT NULL DEFAULT 0, strike INTEGER NOT NULL DEFAULT 0);
	 CREATE INDEX bans_host ON bans(host, addr);
	 CREATE TABLE auth_failures (host TEXT NOT NULL, ip TEXT NOT NULL, kind TEXT NOT NULL, user TEXT NOT NULL DEFAULT '', ts INTEGER NOT NULL, count INTEGER NOT NULL);
	 CREATE INDEX auth_failures_host ON auth_failures(host, ts)`,
	// v21: WAF 事件
	`CREATE TABLE waf_events (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, site_id INTEGER NOT NULL DEFAULT 0, ts INTEGER NOT NULL,
		ip TEXT NOT NULL, uri TEXT NOT NULL, blocked INTEGER NOT NULL, rules TEXT NOT NULL DEFAULT '[]');
	 CREATE INDEX waf_events_host ON waf_events(host, site_id, id)`,
	// v22: 容器流量（按小时）、限速与配额
	`CREATE TABLE traffic_hourly (host TEXT NOT NULL, key TEXT NOT NULL, hour INTEGER NOT NULL, rx INTEGER NOT NULL, tx INTEGER NOT NULL,
		PRIMARY KEY(host, key, hour)) WITHOUT ROWID;
	 CREATE TABLE traffic_limits (host TEXT NOT NULL, container TEXT NOT NULL, spec TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'ok',
		month TEXT NOT NULL DEFAULT '', PRIMARY KEY(host, container))`,
	// v23: Cloudflare 隧道、经隧道公开的网站、Zero Trust Access；封禁同步到 Cloudflare 的规则 id
	`CREATE TABLE cf_tunnels (host TEXT PRIMARY KEY, integration TEXT NOT NULL, account TEXT NOT NULL, tunnel_id TEXT NOT NULL, name TEXT NOT NULL,
		token_enc BLOB NOT NULL, created_at INTEGER NOT NULL);
	 CREATE TABLE cf_tunnel_routes (host TEXT NOT NULL, site_id INTEGER NOT NULL, spec TEXT NOT NULL, PRIMARY KEY(host, site_id));
	 CREATE TABLE cf_access (domain TEXT PRIMARY KEY, spec TEXT NOT NULL);
	 ALTER TABLE bans ADD COLUMN cf_rule TEXT NOT NULL DEFAULT ''`,
	// v24: Wharf 联动——配对、经 Wharf 公开的网站、用户对应、对接请求防重放、免登录票据；封禁与 Wharf 的对应
	`CREATE TABLE wharf_link (id INTEGER PRIMARY KEY CHECK (id = 1), url TEXT NOT NULL, name TEXT NOT NULL, fp TEXT NOT NULL, token_enc BLOB NOT NULL,
		inbound_hash TEXT NOT NULL, scopes TEXT NOT NULL, target_ips TEXT NOT NULL DEFAULT '[]', alerts_primary TEXT NOT NULL DEFAULT '',
		paired_at INTEGER NOT NULL, last_seen INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'active');
	 CREATE TABLE wharf_publish (host TEXT NOT NULL, site_id INTEGER NOT NULL, spec TEXT NOT NULL, PRIMARY KEY(host, site_id));
	 CREATE TABLE wharf_user_map (wharf_user TEXT PRIMARY KEY, berth_user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE);
	 CREATE TABLE wharf_nonces (nonce TEXT PRIMARY KEY, seen_at INTEGER NOT NULL);
	 CREATE TABLE wharf_sso (ticket_hash TEXT PRIMARY KEY, user_id INTEGER NOT NULL, return_to TEXT NOT NULL, expires_at INTEGER NOT NULL);
	 ALTER TABLE bans ADD COLUMN wharf_ban TEXT NOT NULL DEFAULT ''`,
	// v25: 虚拟机——自动生成的 root 密码（加密）；个人 SSH 公钥（建机、开发容器、工作区用）
	`CREATE TABLE vm_secrets (host TEXT NOT NULL, name TEXT NOT NULL, password_enc BLOB NOT NULL, created_at INTEGER NOT NULL, PRIMARY KEY(host, name));
	 CREATE TABLE user_ssh_keys (id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		name TEXT NOT NULL, key TEXT NOT NULL, created_at INTEGER NOT NULL)`,
	// v26: 远程桌面连接库（凭证加密）；撤销用的旧版本
	`CREATE TABLE rd_connections (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL, grp TEXT NOT NULL DEFAULT '', spec TEXT NOT NULL,
		secret_enc BLOB, created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, last_used INTEGER NOT NULL DEFAULT 0);
	 CREATE TABLE rd_undo (id INTEGER PRIMARY KEY, spec TEXT NOT NULL, secret_enc BLOB)`,
	// v27: Wharf 映射（为应用的对外端口，例如 RustDesk 中继）
	`CREATE TABLE wharf_app_publish (host TEXT NOT NULL, name TEXT NOT NULL, spec TEXT NOT NULL, PRIMARY KEY(host, name))`,
	// v28: 应用的访问方式（只在本机 / 内网 / 对外）
	`ALTER TABLE apps ADD COLUMN access TEXT NOT NULL DEFAULT ''`,
	// v29: 每个容器/虚拟机的入站来源与出站去向（按小时），以及对端的反查名和国家
	`CREATE TABLE flows_hourly (host TEXT NOT NULL, obj TEXT NOT NULL, dir TEXT NOT NULL, peer TEXT NOT NULL, proto TEXT NOT NULL, port INTEGER NOT NULL,
		hour INTEGER NOT NULL, conns INTEGER NOT NULL, bytes INTEGER NOT NULL, PRIMARY KEY(host, obj, dir, peer, proto, port, hour)) WITHOUT ROWID;
	 CREATE INDEX flows_hourly_hour ON flows_hourly(host, hour);
	 CREATE TABLE flow_peers (ip TEXT PRIMARY KEY, ptr TEXT NOT NULL DEFAULT '', country TEXT NOT NULL DEFAULT '', seen INTEGER NOT NULL)`,
	// v30: DDNS 的地址来源（自动 / 指定网口 / Wharf / 固定）
	`ALTER TABLE ddns ADD COLUMN source TEXT NOT NULL DEFAULT 'auto'`,
	// v31: 开发工作区（网页 VS Code + 开发容器）；code-server 密码加密
	`CREATE TABLE dev_workspaces (host TEXT NOT NULL, name TEXT NOT NULL, spec TEXT NOT NULL, secret_enc BLOB,
		created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, PRIMARY KEY(host, name))`,
	// v32: Claude Code 工作区——令牌（加密）、任务队列（结果、改动的前后快照）；通知多一类“开发”
	`ALTER TABLE dev_workspaces ADD COLUMN claude_enc BLOB;
	 CREATE TABLE claude_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, ws TEXT NOT NULL, prompt TEXT NOT NULL,
		mode TEXT NOT NULL DEFAULT 'auto', parent INTEGER NOT NULL DEFAULT 0, status TEXT NOT NULL, session TEXT NOT NULL DEFAULT '',
		created_by TEXT NOT NULL DEFAULT '', created_by_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
		started_at INTEGER NOT NULL DEFAULT 0, finished_at INTEGER NOT NULL DEFAULT 0, log TEXT NOT NULL DEFAULT '',
		result TEXT NOT NULL DEFAULT '', snap_before TEXT NOT NULL DEFAULT '', snap_after TEXT NOT NULL DEFAULT '', reverted INTEGER NOT NULL DEFAULT 0);
	 CREATE INDEX claude_tasks_ws ON claude_tasks(host, ws, id);
	 UPDATE notify_channels SET categories = json_insert(categories, '$[#]', 'dev') WHERE json_valid(categories) AND categories NOT LIKE '%"dev"%'`,
	// v33: 已有的 Proxmox VE 集群（API 令牌加密；证书指纹固定）
	`CREATE TABLE pve_clusters (id TEXT PRIMARY KEY, name TEXT NOT NULL, url TEXT NOT NULL, token_id TEXT NOT NULL,
		fingerprint TEXT NOT NULL DEFAULT '', secret_enc BLOB NOT NULL, created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL)`,
	// v34: 计划任务（脚本或 Berth 操作）与执行记录；通知多一类“系统”
	`CREATE TABLE cron_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, host TEXT NOT NULL, spec TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1,
		created_by TEXT NOT NULL DEFAULT '', created_by_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
	 CREATE TABLE cron_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, job_id INTEGER NOT NULL, started_at INTEGER NOT NULL, ms INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL, code INTEGER NOT NULL DEFAULT 0, output TEXT NOT NULL DEFAULT '', trigger TEXT NOT NULL DEFAULT 'schedule', change_id TEXT NOT NULL DEFAULT '');
	 CREATE INDEX cron_runs_job ON cron_runs(job_id, id);
	 UPDATE notify_channels SET categories = json_insert(categories, '$[#]', 'system') WHERE json_valid(categories) AND categories NOT LIKE '%"system"%'`,
	// v35: 配额（按用户）与对象归属（谁建的站点、项目、虚拟机、工作区及其大小）
	`ALTER TABLE users ADD COLUMN quota TEXT NOT NULL DEFAULT '';
	 CREATE TABLE owners (host TEXT NOT NULL, type TEXT NOT NULL, id TEXT NOT NULL, user_id INTEGER NOT NULL,
		cpus REAL NOT NULL DEFAULT 0, mem_mb INTEGER NOT NULL DEFAULT 0, disk_gb INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL,
		PRIMARY KEY(host, type, id));
	 CREATE INDEX owners_user ON owners(user_id)`,
	// v36: 简要模式（只看应用入口墙的账号）；文件分享链接
	`ALTER TABLE users ADD COLUMN simple INTEGER NOT NULL DEFAULT 0;
	 CREATE TABLE shares (id TEXT PRIMARY KEY, host TEXT NOT NULL, path TEXT NOT NULL, root TEXT NOT NULL, created_by TEXT NOT NULL DEFAULT '',
		created_by_id INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL, max_downloads INTEGER NOT NULL DEFAULT 0,
		downloads INTEGER NOT NULL DEFAULT 0, pw_hash TEXT NOT NULL DEFAULT '')`,
	// v37: 第三方扩展（清单、授予的范围、令牌、端口）
	`CREATE TABLE extensions (id TEXT PRIMARY KEY, manifest TEXT NOT NULL, source TEXT NOT NULL, scopes TEXT NOT NULL, token_id INTEGER NOT NULL DEFAULT 0,
		port INTEGER NOT NULL, installed_by TEXT NOT NULL DEFAULT '', installed_at INTEGER NOT NULL)`,
	// v38: 令牌多一类“扩展”（ext）：重建 tokens 表以放宽 kind 的约束
	`CREATE TABLE tokens_new (
		id INTEGER PRIMARY KEY AUTOINCREMENT, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		kind TEXT NOT NULL CHECK (kind IN ('pat','agt','cli','ext')), name TEXT NOT NULL, token_hash TEXT NOT NULL UNIQUE,
		prefix TEXT NOT NULL, scopes TEXT NOT NULL, limits TEXT NOT NULL DEFAULT '{}',
		ip_allow TEXT NOT NULL DEFAULT '[]', danger_policy TEXT NOT NULL DEFAULT 'run' CHECK (danger_policy IN ('run','confirm','readonly')),
		rate_per_min INTEGER NOT NULL DEFAULT 120, note TEXT NOT NULL DEFAULT '',
		not_before INTEGER NOT NULL DEFAULT 0, expires_at INTEGER NOT NULL DEFAULT 0,
		last_used_at INTEGER NOT NULL DEFAULT 0, last_ip TEXT NOT NULL DEFAULT '', uses INTEGER NOT NULL DEFAULT 0,
		revoked INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL);
	 INSERT INTO tokens_new SELECT id, user_id, kind, name, token_hash, prefix, scopes, limits, ip_allow, danger_policy, rate_per_min, note,
		not_before, expires_at, last_used_at, last_ip, uses, revoked, created_at FROM tokens;
	 DROP TABLE tokens;
	 ALTER TABLE tokens_new RENAME TO tokens`,
	// v39: AI 助手——每人自己的密钥（加密）与每月用量
	`CREATE TABLE ai_settings (user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE, provider TEXT NOT NULL, base_url TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '', monthly_limit INTEGER NOT NULL DEFAULT 0, key_enc BLOB NOT NULL);
	 CREATE TABLE ai_usage (user_id INTEGER NOT NULL, month TEXT NOT NULL, tokens_in INTEGER NOT NULL DEFAULT 0, tokens_out INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(user_id, month))`,
	// v40: 项目（Node、Python、Go、Java、自定义命令，由 systemd 守护）
	`CREATE TABLE projects (host TEXT NOT NULL, name TEXT NOT NULL, spec TEXT NOT NULL, created_by TEXT NOT NULL DEFAULT '', created_at INTEGER NOT NULL, PRIMARY KEY(host, name))`,
	// v41: 原生数据库（R4）——实例是 Berth 装在宿主机上的服务器（引擎和版本），或者 SQLite 文件
	`ALTER TABLE databases ADD COLUMN native TEXT NOT NULL DEFAULT ''`,
	// v42: 域名注册到期日（RDAP 缓存）与令牌活动（AI 在做什么）
	`CREATE TABLE domain_rdap (root TEXT PRIMARY KEY, expires INTEGER NOT NULL DEFAULT 0, registrar TEXT NOT NULL DEFAULT '', checked INTEGER NOT NULL, error TEXT NOT NULL DEFAULT '');
	 CREATE TABLE token_activity (id INTEGER PRIMARY KEY, token_id INTEGER NOT NULL, ts INTEGER NOT NULL, method TEXT NOT NULL, kind TEXT NOT NULL DEFAULT '',
		host TEXT NOT NULL DEFAULT '', obj_type TEXT NOT NULL DEFAULT '', obj_id TEXT NOT NULL DEFAULT '', change_id TEXT NOT NULL DEFAULT '', ok INTEGER NOT NULL DEFAULT 1, summary TEXT NOT NULL DEFAULT '');
	 CREATE INDEX token_activity_tok ON token_activity(token_id, ts);
	 CREATE INDEX token_activity_obj ON token_activity(host, obj_type, obj_id, ts)`,
	// v43: 插件（api 1）——扩展表记下运行方式、完整清单、签名者、加密的设置和安装包；每个插件 ID 记住第一次见到的签名钥匙
	`ALTER TABLE extensions ADD COLUMN kind TEXT NOT NULL DEFAULT 'container';
	 ALTER TABLE extensions ADD COLUMN plugin TEXT NOT NULL DEFAULT '';
	 ALTER TABLE extensions ADD COLUMN signer TEXT NOT NULL DEFAULT '';
	 ALTER TABLE extensions ADD COLUMN settings_enc BLOB;
	 ALTER TABLE extensions ADD COLUMN pkg TEXT NOT NULL DEFAULT '';
	 CREATE TABLE plugin_keys (id TEXT PRIMARY KEY, signer TEXT NOT NULL, first_seen INTEGER NOT NULL)`,
	// v44: 常用命令（终端、SSH、远程桌面里一键插入）——每人自己的，或团队共享的
	`CREATE TABLE snippets (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, shared INTEGER NOT NULL DEFAULT 0,
		name TEXT NOT NULL, command TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', folder TEXT NOT NULL DEFAULT '', danger INTEGER NOT NULL DEFAULT 0,
		uses INTEGER NOT NULL DEFAULT 0, last_used INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL);
	 CREATE INDEX snippets_user ON snippets(user_id)`,
}
