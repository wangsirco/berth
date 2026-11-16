package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

func table() *tabwriter.Writer { return tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0) }

func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}

func bytesH(n int64) string {
	f := float64(n)
	for _, u := range []string{"B", "KB", "MB", "GB", "TB"} {
		if f < 1024 || u == "TB" {
			if u == "B" {
				return fmt.Sprintf("%d B", n)
			}
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return ""
}

func cmdStatus(args []string) error {
	g := splitGlobal(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	var ov struct {
		Host struct {
			CPU       float64    `json:"cpu"`
			Cores     int        `json:"cores"`
			Load      [3]float64 `json:"load"`
			MemTotal  int64      `json:"mem_total"`
			MemUsed   int64      `json:"mem_used"`
			SwapUsed  int64      `json:"swap_used"`
			SwapTotal int64      `json:"swap_total"`
			Uptime    int64      `json:"uptime"`
		} `json:"host"`
		Version string `json:"version"`
	}
	if err := c.do("GET", "/api/v1/hosts/"+g.host+"/overview", nil, &ov); err != nil {
		return err
	}
	var sb struct {
		Online bool
		Queue  struct{ Running, Waiting int }
	}
	_ = c.do("GET", "/api/v1/statusbar?host="+g.host, nil, &sb)
	if g.json {
		return printJSON(map[string]any{"overview": ov, "statusbar": sb})
	}
	h := ov.Host
	fmt.Printf("%s  %s  berth %s\n", g.host, map[bool]string{true: "online", false: "offline"}[sb.Online], ov.Version)
	fmt.Printf("cpu     %.0f%% of %d cores, load %.2f\n", h.CPU, h.Cores, h.Load[0])
	fmt.Printf("memory  %s / %s, swap %s / %s\n", bytesH(h.MemUsed), bytesH(h.MemTotal), bytesH(h.SwapUsed), bytesH(h.SwapTotal))
	fmt.Printf("uptime  %s\n", (time.Duration(h.Uptime) * time.Second).String())
	fmt.Printf("queue   %d running, %d waiting\n", sb.Queue.Running, sb.Queue.Waiting)
	return nil
}

func cmdPs(args []string) error {
	g := splitGlobal(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			Name, Image, State, Status, Project string
			Ports                               []struct {
				Private, Public int
				Proto, IP       string
			}
		}
	}
	if err := c.do("GET", "/api/v1/hosts/"+g.host+"/containers", nil, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out.Items)
	}
	tw := table()
	fmt.Fprintln(tw, "NAME\tSTATE\tIMAGE\tPORTS\tPROJECT")
	for _, ct := range out.Items {
		var ports []string
		for _, p := range ct.Ports {
			if p.Public > 0 {
				ports = append(ports, fmt.Sprintf("%s:%d→%d", p.IP, p.Public, p.Private))
			} else {
				ports = append(ports, strconv.Itoa(p.Private))
			}
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ct.Name, ct.Status, ct.Image, strings.Join(ports, ","), ct.Project)
	}
	return tw.Flush()
}

func cmdLogs(args []string) error {
	g := splitGlobal(args)
	fs := flag.NewFlagSet("logs", flag.ContinueOnError)
	follow := fs.Bool("f", false, "follow")
	tail := fs.Int("tail", 200, "lines")
	if err := fs.Parse(reorder(g.rest)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: berth logs <container> [-f] [--tail N]")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	q := fmt.Sprintf("?tail=%d", *tail)
	if *follow {
		q += "&follow=1"
	}
	return c.stream("/api/v1/hosts/"+g.host+"/containers/"+fs.Arg(0)+"/logs"+q, func(ev, data string) bool {
		if ev != "log" {
			return ev != "end"
		}
		var l struct{ T, S, L string }
		if json.Unmarshal([]byte(data), &l) == nil {
			w := io.Writer(os.Stdout)
			if l.S == "err" {
				w = os.Stderr
			}
			fmt.Fprintln(w, l.L)
		}
		return true
	})
}

// reorder puts flags before positionals so `logs n8n -f` works with package flag.
func reorder(args []string) []string {
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		if strings.HasPrefix(args[i], "-") {
			flags = append(flags, args[i])
			if args[i] == "--tail" || args[i] == "-tail" {
				if i+1 < len(args) {
					flags = append(flags, args[i+1])
					i++
				}
			}
		} else {
			pos = append(pos, args[i])
		}
	}
	return append(flags, pos...)
}

func cmdChanges(args []string) error {
	g := splitGlobal(args)
	limit := 20
	for i, a := range g.rest {
		if a == "--limit" && i+1 < len(g.rest) {
			limit, _ = strconv.Atoi(g.rest[i+1])
		}
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var out struct {
		Items []struct {
			ID      string            `json:"id"`
			TS      int64             `json:"ts"`
			Status  string            `json:"status"`
			Summary map[string]string `json:"summary"`
			Actor   struct{ Name, Via string }
			Undo    json.RawMessage `json:"undo"`
		}
	}
	if err := c.do("GET", "/api/v1/changes?limit="+strconv.Itoa(limit), nil, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out.Items)
	}
	tw := table()
	fmt.Fprintln(tw, "TIME\tWHO\tSTATUS\tCHANGE\tID")
	for _, ch := range out.Items {
		mark := ""
		if len(ch.Undo) > 0 && string(ch.Undo) != "null" && ch.Status == "done" {
			mark = " ↺"
		}
		fmt.Fprintf(tw, "%s\t%s (%s)\t%s\t%s\t%s%s\n", time.Unix(ch.TS, 0).Format("01-02 15:04"), ch.Actor.Name, ch.Actor.Via, ch.Status, ch.Summary[c.lang], ch.ID, mark)
	}
	return tw.Flush()
}

func cmdRevert(args []string) error {
	g := splitGlobal(args)
	if len(g.rest) != 1 {
		return errors.New("usage: berth revert <change-id>")
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var orig changeOut
	if err := c.do("GET", "/api/v1/changes/"+g.rest[0], nil, &orig); err != nil {
		return err
	}
	if !g.yes {
		fmt.Printf("Undo: %s\n", orig.Summary[c.lang])
		if a := strings.ToLower(ask("Go ahead? [y/N] ")); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	var out changeOut
	if err := c.do("POST", "/api/v1/changes/"+g.rest[0]+"/revert", nil, &out); err != nil {
		return err
	}
	if g.json {
		return printJSON(out)
	}
	fmt.Printf("✓ undone (%s)\n", out.ID)
	return nil
}

func cmdTasks(args []string) error {
	g := splitGlobal(args)
	c, err := newClient()
	if err != nil {
		return err
	}
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := c.do("GET", "/api/v1/tasks?host="+g.host, nil, &raw); err != nil {
		return err
	}
	if g.json {
		return printJSON(raw.Items)
	}
	tw := table()
	fmt.Fprintln(tw, "ID\tKIND\tSTATUS\tBY")
	for _, t := range raw.Items {
		fmt.Fprintf(tw, "%v\t%v\t%v\t%v\n", t["id"], t["kind"], t["status"], t["created_by"])
	}
	return tw.Flush()
}
