// Berth plugin example: iperf3 with a page. It runs as its own user (berth-p-iperf3) with a
// read-only system; Berth installed the iperf3 package for it and gives it $PORT for its page.
package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"sync"
	"time"
)

var (
	mu     sync.Mutex
	server *exec.Cmd
	hostRe = regexp.MustCompile(`^[A-Za-z0-9.:-]{1,253}$`)
)

type result struct {
	Intervals []float64 `json:"intervals"` // Mbit/s each second
	Mbps      float64   `json:"mbps"`
	Retrans   int       `json:"retransmits"`
	Error     string    `json:"error,omitempty"`
}

func run(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	port, _ := strconv.Atoi(r.URL.Query().Get("port"))
	secs, _ := strconv.Atoi(r.URL.Query().Get("seconds"))
	if !hostRe.MatchString(host) || net.ParseIP(host) == nil && len(host) < 3 {
		http.Error(w, "bad host", 400)
		return
	}
	if port < 1 || port > 65535 {
		port = 5201
	}
	if secs < 1 || secs > 30 {
		secs = 5
	}
	args := []string{"-c", host, "-p", strconv.Itoa(port), "-t", strconv.Itoa(secs), "-J"}
	if r.URL.Query().Get("reverse") == "1" {
		args = append(args, "-R")
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(secs+15)*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "iperf3", args...).Output()
	var j struct {
		Intervals []struct {
			Sum struct {
				BPS float64 `json:"bits_per_second"`
			} `json:"sum"`
		} `json:"intervals"`
		End struct {
			Sent struct {
				BPS     float64 `json:"bits_per_second"`
				Retrans int     `json:"retransmits"`
			} `json:"sum_sent"`
			Recv struct {
				BPS float64 `json:"bits_per_second"`
			} `json:"sum_received"`
		} `json:"end"`
		Error string `json:"error"`
	}
	json.Unmarshal(out, &j)
	res := result{Error: j.Error, Retrans: j.End.Sent.Retrans, Mbps: j.End.Recv.BPS / 1e6, Intervals: []float64{}}
	for _, i := range j.Intervals {
		res.Intervals = append(res.Intervals, i.Sum.BPS/1e6)
	}
	if len(out) == 0 && res.Error == "" {
		res.Error = "iperf3 did not answer (is it installed?)"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func serverState(w http.ResponseWriter, r *http.Request) {
	mu.Lock()
	defer mu.Unlock()
	port := os.Getenv("BERTH_SETTING_SERVER_PORT")
	if port == "" {
		port = "5201"
	}
	if r.Method == "POST" {
		on := r.URL.Query().Get("on") == "1"
		if on && server == nil {
			server = exec.Command("iperf3", "-s", "-p", port)
			if err := server.Start(); err != nil {
				server = nil
				http.Error(w, err.Error(), 500)
				return
			}
			go func(c *exec.Cmd) {
				c.Wait()
				mu.Lock()
				if server == c {
					server = nil
				}
				mu.Unlock()
			}(server)
		} else if !on && server != nil {
			server.Process.Kill()
			server = nil
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"running": server != nil, "port": port})
}

func main() {
	http.HandleFunc("/run", run)
	http.HandleFunc("/server", serverState)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(page))
	})
	http.ListenAndServe("127.0.0.1:"+os.Getenv("PORT"), nil)
}

const page = `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<style>
:root{--acc:#1f6f5c;--line:#e4e0d6;--mute:#6f6b62;--bg:#f7f5f0;--card:#fffdf9;--fg:#26251f}
@media (prefers-color-scheme:dark){:root{--acc:#5cc1a3;--line:#34322c;--mute:#a19d92;--bg:#171714;--card:#22211d;--fg:#ecebe5}}
body{font:14px/1.5 system-ui,-apple-system,"PingFang SC",sans-serif;margin:0;padding:20px;background:var(--bg);color:var(--fg)}
.card{background:var(--card);border:1px solid var(--line);border-radius:12px;padding:16px;margin-bottom:14px;max-width:760px}
h2{font-size:15px;margin:0 0 10px} label{display:inline-flex;gap:6px;align-items:center;margin:0 12px 8px 0;color:var(--mute);font-size:13px}
input{font:inherit;padding:5px 8px;border:1px solid var(--line);border-radius:8px;background:var(--bg);color:var(--fg)}
button{font:inherit;padding:6px 14px;border-radius:8px;border:0;background:var(--acc);color:#fff;cursor:pointer} button:disabled{opacity:.5}
.big{font-size:28px;font-weight:700} .mute{color:var(--mute);font-size:12.5px}
.bars{display:flex;align-items:flex-end;gap:4px;height:120px;margin-top:10px;border-bottom:1px solid var(--line)}
.bars div{flex:1;background:var(--acc);border-radius:4px 4px 0 0;min-height:2px}
</style></head><body>
<div class="card"><h2>测这台到另一台 · Test to another machine</h2>
<label>地址 Host <input id="h" placeholder="speedtest.example.com" size="24"></label>
<label>端口 Port <input id="p" value="5201" size="5"></label>
<label>秒 Seconds <input id="s" value="5" size="3"></label>
<label><input type="checkbox" id="r"> 反向（测下载）Reverse</label><br>
<button id="go">开始 Start</button> <span id="st" class="mute"></span>
<div id="out" style="margin-top:12px"></div></div>
<div class="card"><h2>测速服务端 · iperf3 server</h2>
<p class="mute">打开后别人可以用 <code>iperf3 -c 这台的地址 -p <span id="sp"></span></code> 来测这台。记得在防火墙放行这个端口。</p>
<button id="srv">…</button></div>
<script>
const $=(i)=>document.getElementById(i)
async function srv(on){const r=await fetch('server'+(on===undefined?'':'?on='+(on?1:0)),{method:on===undefined?'GET':'POST'});const j=await r.json();$('sp').textContent=j.port;$('srv').textContent=j.running?'关闭 Stop':'打开 Start';$('srv').onclick=()=>srv(!j.running)}
srv()
$('go').onclick=async()=>{$('go').disabled=true;$('st').textContent='测试中… Testing…';$('out').innerHTML=''
 try{const q=new URLSearchParams({host:$('h').value.trim(),port:$('p').value,seconds:$('s').value,reverse:$('r').checked?1:0})
 const j=await (await fetch('run?'+q)).json()
 if(j.error){$('out').innerHTML='<p style="color:#b5502a">'+j.error.replace(/</g,'&lt;')+'</p>'}else{
  const max=Math.max(...j.intervals,1)
  $('out').innerHTML='<div class="big">'+j.mbps.toFixed(1)+' Mbit/s</div><div class="mute">重传 retransmits: '+j.retransmits+'</div><div class="bars">'+j.intervals.map(v=>'<div title="'+v.toFixed(1)+' Mbit/s" style="height:'+(v/max*100)+'%"></div>').join('')+'</div>'}
 }finally{$('go').disabled=false;$('st').textContent=''}}
</script></body></html>`
