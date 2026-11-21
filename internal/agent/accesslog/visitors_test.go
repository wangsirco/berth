package accesslog

import "testing"

func TestBotKind(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15": "",
		"Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)":          "search",
		"Mozilla/5.0 AppleWebKit/537.36 (KHTML, like Gecko; compatible; GPTBot/1.2)":         "ai",
		"Mozilla/5.0 (compatible; ClaudeBot/1.0; +claudebot@anthropic.com)":                  "ai",
		"facebookexternalhit/1.1": "social",
		"curl/8.5.0":              "tool",
		"Uptime-Kuma/1.23":        "monitor",
		"Mozilla/5.0 zgrab/0.x":   "scanner",
		"":                        "other",
	}
	for ua, want := range cases {
		if got := BotKind(ua); got != want {
			t.Errorf("%q: %q, want %q", ua, got, want)
		}
	}
}

func TestTunnelVisitorAndNew(t *testing.T) {
	e := &entry{TS: 1_800_000_000, Status: 200}
	e.Request.ClientIP = "172.19.0.2" // cloudflared on the edge network
	e.Request.Headers = map[string][]string{"Cf-Connecting-Ip": {"203.0.113.7"}, "User-Agent": {"Mozilla/5.0 Firefox/140.0"}}
	if ip := visitorIP(e); ip != "203.0.113.7" {
		t.Fatalf("tunnel visitor shows as %s", ip)
	}
	seen := &seenSet{}
	m := newMinute("a.example", 1_800_000_000)
	m.add(e, nil, seen)
	m.add(e, nil, seen)
	bot := &entry{TS: 1_800_000_000, Status: 200}
	bot.Request.ClientIP = "66.249.66.1"
	bot.Request.Headers = map[string][]string{"User-Agent": {"Googlebot/2.1"}}
	m.add(bot, nil, seen)
	m.close()
	if m.New != 1 || m.HumanReq != 2 || m.Humans != 1 || m.Bots != 1 || m.BotKinds["search"] != 1 {
		t.Fatalf("new %d humanReq %d humans %d bots %d kinds %v", m.New, m.HumanReq, m.Humans, m.Bots, m.BotKinds)
	}
}
