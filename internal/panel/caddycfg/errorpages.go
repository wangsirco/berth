package caddycfg

// The pages visitors see when something is wrong: blocked, not found, too many requests, the
// application behind the site not answering, or the site stopped. Plain, bilingual (the visitor's
// language is unknown), no panel branding, and nothing the visitor sent is echoed back — only the
// request id and the time, for whoever runs the site.

import (
	"strconv"
	"strings"
)

type errPage struct {
	code               int
	match              string // extra condition on {http.error.message}, e.g. stopped sites
	zh, zhMsg, en, enM string
	icon               string // a small inline SVG path set
}

const (
	iconBlock = `<circle cx="24" cy="24" r="17"/><path d="M12 12l24 24"/>`
	iconFind  = `<circle cx="21" cy="21" r="12"/><path d="M30 30l9 9"/><path d="M17 21h8"/>`
	iconClock = `<circle cx="24" cy="24" r="17"/><path d="M24 14v10l7 4"/>`
	iconPlug  = `<path d="M18 6v10M30 6v10M14 16h20v8a10 10 0 0 1-20 0z"/><path d="M24 34v8"/>`
	iconPause = `<circle cx="24" cy="24" r="17"/><path d="M20 17v14M28 17v14"/>`
	iconWarn  = `<path d="M24 7l19 33H5z"/><path d="M24 20v9M24 34v1"/>`
)

// StoppedMessage marks the error a stopped site answers with.
const StoppedMessage = "site stopped"

var errPages = []errPage{
	{503, StoppedMessage, "网站暂停访问", "这个网站已被管理员暂时停用，请稍后再来。", "This site is paused", "The owner has paused this site for now. Please come back later.", iconPause},
	{403, "", "访问被拦截", "你的请求被这个网站的安全规则拦下了。如果你认为这是误会，请把下面的请求编号告诉网站管理员。", "Access blocked", "This site's security rules stopped your request. If you think this is a mistake, send the request ID below to the site's owner.", iconBlock},
	{404, "", "页面不存在", "你要找的页面不在这里：可能已经删除、改了地址，或者链接写错了。", "Page not found", "The page you are looking for is not here. It may have moved, been removed, or the link may be wrong.", iconFind},
	{429, "", "请求太频繁", "短时间内的访问次数太多了，请稍等一会儿再试。", "Too many requests", "You have sent too many requests in a short time. Please wait a moment and try again.", iconClock},
	{502, "", "网站暂时打不开", "网站后面的程序没有响应，可能正在重启或维护。请过一会儿刷新试试。", "The site is not answering", "The application behind this site is not responding; it may be restarting. Please refresh in a little while.", iconPlug},
	{504, "", "网站响应超时", "网站后面的程序太久没有回应，请过一会儿刷新试试。", "The site took too long", "The application behind this site took too long to answer. Please refresh in a little while.", iconPlug},
	{503, "", "暂时不可用", "网站正在维护或者还没有部署完成，请稍后再来。", "Temporarily unavailable", "The site is under maintenance or not deployed yet. Please come back later.", iconPause},
	{500, "", "网站出错了", "网站处理你的请求时出了问题，请稍后再试。", "Something went wrong", "The site ran into a problem handling your request. Please try again later.", iconWarn},
}

func errorHTML(p errPage, code string) string {
	css := `*{box-sizing:border-box}body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;font:15px/1.6 -apple-system,BlinkMacSystemFont,"PingFang SC","Microsoft YaHei","Segoe UI",sans-serif;background:#f7f5f0;color:#26251f;padding:24px}` +
		`main{max-width:560px;width:100%;background:#fffdf9;border:1px solid #e4e0d6;border-radius:18px;padding:36px 32px}` +
		`svg{width:44px;height:44px;fill:none;stroke:#6f6b62;stroke-width:3;stroke-linecap:round;stroke-linejoin:round}` +
		`.code{font:600 13px/1 ui-monospace,SFMono-Regular,Menlo,monospace;color:#6f6b62;letter-spacing:.08em;margin:18px 0 6px}` +
		`h1{font-size:22px;margin:0 0 6px}h2{font-size:15px;font-weight:600;margin:22px 0 4px;color:#45433b}p{margin:0;color:#6f6b62}` +
		`.meta{margin-top:26px;padding-top:14px;border-top:1px solid #e4e0d6;font:12px/1.7 ui-monospace,SFMono-Regular,Menlo,monospace;color:#8b877c;word-break:break-all}` +
		`@media (prefers-color-scheme:dark){body{background:#171714;color:#ecebe5}main{background:#22211d;border-color:#34322c}h2{color:#d6d4cc}p{color:#a19d92}svg{stroke:#a19d92}.meta{border-color:#34322c;color:#8b877c}}`
	var b strings.Builder
	b.WriteString(`<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="robots" content="noindex">`)
	b.WriteString(`<title>` + code + ` · ` + p.zh + ` / ` + p.en + `</title><style>` + css + `</style></head><body><main>`)
	b.WriteString(`<svg viewBox="0 0 48 48" aria-hidden="true">` + p.icon + `</svg>`)
	b.WriteString(`<div class="code">` + code + `</div><h1>` + p.zh + `</h1><p>` + p.zhMsg + `</p>`)
	b.WriteString(`<h2 lang="en">` + p.en + `</h2><p lang="en">` + p.enM + `</p>`)
	b.WriteString(`<div class="meta">{http.request.uuid}<br>{time.now.common_log}</div></main></body></html>`)
	return b.String()
}

// errorRoutes: what a server answers when a handler fails or refuses. Matched on the status
// (and, for stopped sites, the error's message); anything else falls back to the 500 page.
func errorRoutes() []any {
	var out []any
	for _, p := range errPages {
		expr := "{http.error.status_code} == " + strconv.Itoa(p.code)
		if p.match != "" {
			expr += ` && {http.error.message} == "` + p.match + `"`
		}
		out = append(out, obj{
			"match":    []any{obj{"expression": expr}},
			"handle":   []any{errResponse(p, strconv.Itoa(p.code))},
			"terminal": true,
		})
	}
	// any other 4xx/5xx: the generic page with its real code
	gen := errPages[len(errPages)-1]
	out = append(out, obj{"handle": []any{errResponse(errPage{zh: gen.zh, zhMsg: gen.zhMsg, en: gen.en, enM: gen.enM, icon: gen.icon}, "{http.error.status_code}")}})
	return out
}

func errResponse(p errPage, code string) obj {
	return obj{"handler": "static_response", "status_code": "{http.error.status_code}", "body": errorHTML(p, code),
		"headers": obj{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}, "X-Content-Type-Options": []string{"nosniff"}}}
}

// refuse: stop with an error so the error page answers (instead of an empty response).
func refuse(code int, msg string) obj {
	return obj{"handler": "error", "status_code": code, "error": msg}
}
