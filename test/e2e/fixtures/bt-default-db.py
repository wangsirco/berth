# Builds a 宝塔 default.db (the tables Berth reads) for the migrate-bt e2e. Usage: bt-default-db.py out.db
import os, sqlite3, sys
if os.path.exists(sys.argv[1]):
    os.remove(sys.argv[1])
db = sqlite3.connect(sys.argv[1])
db.executescript("""
CREATE TABLE sites (id INTEGER PRIMARY KEY, name TEXT, path TEXT, status TEXT, ps TEXT, addtime TEXT, project_type TEXT, project_config TEXT);
INSERT INTO sites VALUES (1, 'btblog.e2e', '/www/wwwroot/btblog.e2e', '1', '', '', 'PHP', '');
INSERT INTO sites VALUES (2, 'btstatic.e2e', '/www/wwwroot/btstatic.e2e', '1', '', '', 'PHP', '');
CREATE TABLE domain (id INTEGER PRIMARY KEY, pid INTEGER, name TEXT, port INTEGER, addtime TEXT);
INSERT INTO domain VALUES (1, 1, 'btblog.e2e', 80, ''), (2, 1, 'www.btblog.e2e', 80, ''), (3, 2, 'btstatic.e2e', 80, '');
CREATE TABLE databases (id INTEGER PRIMARY KEY, pid INTEGER, name TEXT, username TEXT, password TEXT, accept TEXT, ps TEXT, addtime TEXT, type TEXT, sid INTEGER);
INSERT INTO databases VALUES (1, 1, 'btblog', 'btblog', 'bt p''w-e2e', '127.0.0.1', '', '', 'MySQL', 0);
CREATE TABLE config (id INTEGER PRIMARY KEY, mysql_root TEXT);
INSERT INTO config VALUES (1, 'btroot-e2e-pass');
CREATE TABLE crontab (id INTEGER PRIMARY KEY, name TEXT, type TEXT, where1 TEXT, where_hour INTEGER, where_minute INTEGER, echo TEXT, addtime TEXT, status INTEGER, save TEXT, backupTo TEXT, sName TEXT, sBody TEXT, sType TEXT, urladdress TEXT);
INSERT INTO crontab VALUES (1, 'bt e2e clean', 'day', '', 3, 30, 'x', '', 1, '', '', '', 'echo bt-clean', 'toShell', '');
INSERT INTO crontab VALUES (2, 'bt e2e site backup', 'day', '', 1, 0, 'y', '', 1, '3', 'localhost', 'btblog.e2e', '', 'site', '');
""")
db.commit()
