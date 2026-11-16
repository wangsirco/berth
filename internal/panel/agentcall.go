package panel

import (
	"bufio"
	"context"
	"encoding/json"
	"io"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/change"
	"github.com/wangsirco/berth/internal/errs"
)

func (a *App) call(ctx context.Context, host, method, path string, in, out any) error {
	c, err := a.agent(host)
	if err != nil {
		return err
	}
	return c.Do(ctx, method, path, in, out)
}

// streamLines reads an agent NDJSON stream: {"l": "..."} lines go to line, the final
// {"done":true,"error":...} decides the result.
func streamLines(rc io.ReadCloser, line func(string)) error {
	defer rc.Close()
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var m struct {
			L     *string `json:"l"`
			Done  bool    `json:"done"`
			Error *errs.E `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Done {
			if m.Error != nil {
				if m.Error.Status == 0 {
					m.Error.Status = 500
				}
				return m.Error
			}
			return nil
		}
		if m.L != nil {
			line(*m.L)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errs.New(502, "agent.stream_cut")
}

// agentOp runs a streamed agent operation inside a change, logging its output.
func agentOp(env change.Env, path string, in any) error {
	c, err := svc(env).agent(env.Host)
	if err != nil {
		return err
	}
	rc, err := c.Stream(env.Ctx, "POST", path, in)
	if err != nil {
		return err
	}
	return streamLines(rc, func(l string) { env.Log("%s", l) })
}

var _ = agent.StreamEnd{} // the stream shape is defined in internal/agent/stream.go
