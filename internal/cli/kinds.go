package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type prop struct {
	Type string   `json:"type"`
	Desc string   `json:"description"`
	Enum []string `json:"enum"`
}

type paramSchema struct {
	Props    map[string]prop `json:"properties"`
	Required []string        `json:"required"`
}

type kindInfo struct {
	Name   string      `json:"name"`
	Title  string      `json:"title"`
	CLI    string      `json:"cli"`
	Params paramSchema `json:"params"`
	Heavy  bool        `json:"heavy"`
}

var placeholder = regexp.MustCompile(`^\{\{\.(\w+)\}\}$`)

// commandShape reads a kind's CLI template: the words after "berth" up to the first
// placeholder or flag form the command path; standalone {{.x}} words are positional.
func commandShape(k kindInfo) (path string, positional []string) {
	words := strings.Fields(k.CLI)
	var cmd []string
	inPath := true
	for i, w := range words {
		if i == 0 && w == "berth" {
			continue
		}
		if m := placeholder.FindStringSubmatch(w); m != nil {
			inPath = false
			positional = append(positional, m[1])
			continue
		}
		if strings.HasPrefix(w, "{{") || strings.HasPrefix(w, "-") {
			inPath = false
		}
		if inPath {
			cmd = append(cmd, w)
		}
	}
	// a placeholder glued to an {{if}} block ("{{.name}}{{if .volumes}}") is still positional
	for _, w := range words {
		if strings.HasPrefix(w, "{{.") && !placeholder.MatchString(w) {
			if m := regexp.MustCompile(`^\{\{\.(\w+)\}\}`).FindStringSubmatch(w); m != nil {
				positional = append(positional, m[1])
			}
		}
	}
	return strings.Join(cmd, " "), positional
}

// parseArgs maps positional words and --flags onto the kind's params. A value "@file" is
// read from that file; an array param takes every remaining positional word.
func parseArgs(k kindInfo, args []string) (map[string]any, error) {
	_, pos := commandShape(k)
	out := map[string]any{}
	pi := 0
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "--") {
			name, val, hasVal := strings.Cut(a[2:], "=")
			name = strings.ReplaceAll(name, "-", "_")
			p, ok := k.Params.Props[name]
			if !ok {
				return nil, fmt.Errorf("unknown option --%s", name)
			}
			if p.Type == "boolean" && !hasVal {
				out[name] = true
				continue
			}
			if !hasVal {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = args[i]
			}
			v, err := convert(p, val)
			if err != nil {
				return nil, fmt.Errorf("--%s: %w", name, err)
			}
			out[name] = v
			continue
		}
		if pi >= len(pos) {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		name := pos[pi]
		if k.Params.Props[name].Type == "array" {
			list, _ := out[name].([]string)
			out[name] = append(list, a)
			continue
		}
		v, err := convert(k.Params.Props[name], a)
		if err != nil {
			return nil, err
		}
		out[name] = v
		pi++
	}
	for _, r := range k.Params.Required {
		if _, ok := out[r]; !ok {
			return nil, fmt.Errorf("missing %s", r)
		}
	}
	return out, nil
}

func convert(p prop, v string) (any, error) {
	if strings.HasPrefix(v, "@") {
		b, err := os.ReadFile(v[1:])
		if err != nil {
			return nil, err
		}
		v = string(b)
	}
	switch p.Type {
	case "boolean":
		return strconv.ParseBool(v)
	case "integer":
		return strconv.ParseInt(v, 10, 64)
	case "array":
		if strings.HasPrefix(v, "[") {
			var l []any
			err := json.Unmarshal([]byte(v), &l)
			return l, err
		}
		return []string{v}, nil
	case "object":
		var m map[string]any
		err := json.Unmarshal([]byte(v), &m)
		return m, err
	}
	if len(p.Enum) > 0 {
		for _, e := range p.Enum {
			if e == v {
				return v, nil
			}
		}
		return nil, fmt.Errorf("must be one of %s", strings.Join(p.Enum, ", "))
	}
	return v, nil
}

func kindsCachePath(c *client) string {
	dir, _ := os.UserCacheDir()
	host := strings.NewReplacer("https://", "", ":", "_", "/", "_").Replace(c.srv.URL)
	return filepath.Join(dir, "berth", "kinds-"+host+".json")
}

// kinds lists what the signed-in user may run, cached for a day.
func (c *client) kinds(refresh bool) ([]kindInfo, error) {
	p := kindsCachePath(c)
	if st, err := os.Stat(p); err == nil && !refresh && time.Since(st.ModTime()) < 24*time.Hour {
		var ks []kindInfo
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &ks) == nil {
			return ks, nil
		}
	}
	var out struct{ Items []kindInfo }
	if err := c.do("GET", "/api/v1/kinds", nil, &out); err != nil {
		return nil, err
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	b, _ := json.Marshal(out.Items)
	_ = os.WriteFile(p, b, 0o600)
	return out.Items, nil
}

type globalFlags struct {
	host string
	plan bool
	yes  bool
	json bool
	rest []string
}

func splitGlobal(args []string) globalFlags {
	g := globalFlags{host: "local"}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--plan":
			g.plan = true
		case a == "--yes" || a == "-y":
			g.yes = true
		case a == "--json":
			g.json = true
		case a == "--host" && i+1 < len(args):
			g.host = args[i+1]
			i++
		case strings.HasPrefix(a, "--host="):
			g.host = a[7:]
		default:
			g.rest = append(g.rest, a)
		}
	}
	return g
}

// match finds the kind whose command path is the longest prefix of args.
func match(ks []kindInfo, args []string) (*kindInfo, []string) {
	var best *kindInfo
	bestN := 0
	for i := range ks {
		path, _ := commandShape(ks[i])
		words := strings.Fields(path)
		if len(words) == 0 || len(words) > len(args) || len(words) <= bestN {
			continue
		}
		ok := true
		for j, w := range words {
			if args[j] != w {
				ok = false
				break
			}
		}
		if ok {
			best, bestN = &ks[i], len(words)
		}
	}
	if best == nil {
		return nil, nil
	}
	return best, args[bestN:]
}

type changeOut struct {
	ID         string            `json:"id"`
	Status     string            `json:"status"`
	Kind       string            `json:"kind"`
	Equivalent string            `json:"equivalent"`
	Summary    map[string]string `json:"summary"`
	Title      map[string]string `json:"title"`
	Log        string            `json:"log"`
	Result     json.RawMessage   `json:"result"`
	Undo       json.RawMessage   `json:"undo"`
	Plan       struct {
		Risk     string   `json:"risk"`
		Commands []string `json:"commands"`
		Files    []struct {
			Path    string `json:"path"`
			Unified string `json:"unified"`
		} `json:"files"`
		Affected []struct{ Type, ID string } `json:"affected"`
		Undoable bool                        `json:"undoable"`
	} `json:"plan"`
}

func printPlan(c *client, ch changeOut) {
	fmt.Printf("%s\n", ch.Summary[c.lang])
	fmt.Printf("  risk: %s   undoable: %v\n", ch.Plan.Risk, ch.Plan.Undoable)
	for _, a := range ch.Plan.Affected {
		fmt.Printf("  affects: %s %s\n", a.Type, a.ID)
	}
	for _, cmd := range ch.Plan.Commands {
		fmt.Printf("  runs: %s\n", cmd)
	}
	for _, f := range ch.Plan.Files {
		fmt.Print(f.Unified)
	}
}

func runGenerated(args []string) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	g := splitGlobal(args)
	ks, err := c.kinds(false)
	if err != nil {
		return err
	}
	k, rest := match(ks, g.rest)
	if k == nil {
		if ks, err = c.kinds(true); err == nil {
			k, rest = match(ks, g.rest)
		}
		if k == nil {
			return fmt.Errorf("unknown command %q; see `berth commands`", strings.Join(g.rest, " "))
		}
	}
	params, err := parseArgs(*k, rest)
	if err != nil {
		path, pos := commandShape(*k)
		return fmt.Errorf("%v\nusage: berth %s %s", err, path, strings.ToUpper(strings.Join(pos, " ")))
	}
	base := "/api/v1/hosts/" + g.host + "/changes/" + k.Name
	var preview changeOut
	if err := c.do("POST", base+"?plan=1", params, &preview); err != nil {
		return err
	}
	if g.plan {
		if g.json {
			return printJSON(preview)
		}
		printPlan(c, preview)
		return nil
	}
	if (preview.Plan.Risk == "high" || preview.Plan.Risk == "destructive") && !g.yes {
		printPlan(c, preview)
		if a := strings.ToLower(ask("Go ahead? [y/N] ")); a != "y" && a != "yes" {
			return errors.New("cancelled")
		}
	}
	var done changeOut
	if err := c.do("POST", base, params, &done); err != nil {
		return err
	}
	if done.Status == "running" { // queued as a task: follow it
		done, err = c.follow(done.ID)
		if err != nil {
			return err
		}
	}
	if g.json {
		return printJSON(done)
	}
	switch done.Status {
	case "done":
		fmt.Printf("✓ %s (%s)\n", done.Summary[c.lang], done.ID)
		if len(done.Undo) > 0 && string(done.Undo) != "null" {
			fmt.Printf("  undo: berth revert %s\n", done.ID)
		}
	case "pending_approval":
		fmt.Printf("waiting for approval in the panel (%s)\n", done.ID)
	default:
		fmt.Printf("%s: %s\n%s", done.Status, done.Summary[c.lang], done.Log)
		return errors.New("change " + done.Status)
	}
	return nil
}

// follow prints a queued change's task log until it finishes.
func (c *client) follow(id string) (changeOut, error) {
	taskID := "tsk_" + strings.TrimPrefix(id, "chg_")
	seq := 0
	for {
		var logs struct {
			Items []struct {
				Seq  int    `json:"seq"`
				Line string `json:"line"`
			}
		}
		if err := c.do("GET", "/api/v1/tasks/"+taskID+"/logs?after="+strconv.Itoa(seq), nil, &logs); err == nil {
			for _, l := range logs.Items {
				if !strings.HasPrefix(l.Line, "::step ") {
					fmt.Fprintln(os.Stderr, "  "+l.Line)
				}
				seq = l.Seq
			}
		}
		var ch changeOut
		if err := c.do("GET", "/api/v1/changes/"+id, nil, &ch); err != nil {
			return ch, err
		}
		if ch.Status != "running" {
			return ch, nil
		}
		time.Sleep(time.Second)
	}
}

func cmdCommands(args []string) error {
	c, err := newClient()
	if err != nil {
		return err
	}
	ks, err := c.kinds(true)
	if err != nil {
		return err
	}
	sort.Slice(ks, func(i, j int) bool { return ks[i].CLI < ks[j].CLI })
	tw := table()
	for _, k := range ks {
		fmt.Fprintf(tw, "%s\t%s\n", k.CLI, k.Title)
	}
	return tw.Flush()
}
