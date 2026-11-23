package panel

// Instead of a bare 404: someone on the LAN who opens one of the panel's own names (an old
// bookmark, a renamed panel, the name without the entry path) learns what to do. The entry
// path itself is never shown; strangers on the internet still get the plain 404.

import (
	"html"
	"net"
	"net/http"
	"strings"
)

func (a *App) friendlyGate(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet || !strings.Contains(r.Header.Get("Accept"), "text/html") {
		return false
	}
	ip := net.ParseIP(a.clientIP(r))
	if ip == nil || !(privateIP(ip) || ip.IsLoopback()) {
		return false
	}
	host := strings.ToLower(hostOnly(r.Host))
	lan, _ := a.entryNames()
	own := contains(lan, host) || host == a.mainLAN() || host == a.lanIP()
	if !own {
		return false
	}
	main := a.nameURL(a.mainLAN())
	moved := main != "" && !strings.EqualFold(host, a.mainLAN())
	zh := !strings.HasPrefix(strings.ToLower(r.Header.Get("Accept-Language")), "en")
	t := func(z, e string) string {
		if zh {
			return z
		}
		return e
	}
	var b strings.Builder
	b.WriteString(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Berth</title>`)
	b.WriteString(`<style>body{font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC",sans-serif;background:#f7f5f0;color:#26251f;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}main{max-width:520px;margin:24px;background:#fffdf9;border:1px solid #e3ded2;border-radius:12px;padding:24px 28px}h1{font-size:18px;margin:0 0 10px}p{margin:8px 0}code{font-family:ui-monospace,monospace;background:#f1eee6;padding:1px 5px;border-radius:4px}a{color:#1f6f5c}.m{color:#6f6b62;font-size:13px}@media(prefers-color-scheme:dark){body{background:#171714;color:#ecebe5}main{background:#22211d;border-color:#36342e}code{background:#1d1c19}a{color:#5cc1a3}.m{color:#a19d92}}</style><main>`)
	b.WriteString(`<h1>` + t("这里是 Berth 面板", "This is the Berth panel") + `</h1>`)
	if moved {
		b.WriteString(`<p>` + t("面板已经换了新地址：", "The panel has a new address: ") + `<a href="` + html.EscapeString(main) + `"><b>` + html.EscapeString(main) + `</b></a></p>`)
		b.WriteString(`<p>` + t("在新地址上也要用你的安全入口打开（收藏夹里那条以 /… 结尾的地址，把前面换成新地址即可）。", "Open it with your security entry there too (your bookmarked address ending in /…, with the new name in front).") + `</p>`)
		b.WriteString(`<p>` + t("Passkey 跟着名字走：新地址上还没有 Passkey 的话，先在这个旧地址用安全入口登录，登录后会带你去新地址添加。", "Passkeys belong to a name: if the new address has none yet, sign in here with your entry first; afterwards you are taken to add one there.") + `</p>`)
	} else {
		b.WriteString(`<p>` + t("为了安全，面板只在「安全入口」地址上打开：用你收藏的那条以 /… 结尾的地址打开。", "For safety the panel opens only at its security entry: use your bookmarked address ending in /….") + `</p>`)
	}
	b.WriteString(`<p class="m">` + t("忘了安全入口或密码：在服务器上运行 ", "Forgot the entry or the password: on the server run ") + `<code>sudo berth admin</code></p></main>`)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte(b.String()))
	return true
}
