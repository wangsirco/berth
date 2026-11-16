// Package change is Berth's single write path: every mutation (UI, API, MCP, CLI,
// extension, schedule) is a Change that is planned, checked, applied, recorded
// and, where possible, reverted.
package change

import (
	"context"
	"encoding/json"

	"github.com/wangsirco/berth/internal/sign"
)

type Risk string

const (
	RiskLow         Risk = "low"
	RiskMedium      Risk = "medium"
	RiskHigh        Risk = "high"
	RiskDestructive Risk = "destructive"
)

// Actor is who asked for a change. Type: user|pat|agt|cli|extension|schedule|system.
// Via: ui|api|mcp|cli.
type Actor struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Via       string `json:"via"`
	IP        string `json:"ip"`
	AISession string `json:"ai_session,omitempty"`
	Intent    string `json:"intent,omitempty"`
}

type FileDiff struct {
	Path    string `json:"path"`
	Before  string `json:"-"`
	After   string `json:"-"`
	Unified string `json:"unified"`
}

type Object struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Host string `json:"host,omitempty"`
}

type Plan struct {
	Summary  string         `json:"summary"`
	SummaryP map[string]any `json:"summary_p,omitempty"`
	Files    []FileDiff     `json:"files"`
	Commands []string       `json:"commands"`
	Affected []Object       `json:"affected"`
	Risk     Risk           `json:"risk"`
	Undoable bool           `json:"undoable"`
}

type UndoRecipe struct {
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params"`
}

type Change struct {
	ID         string          `json:"id"`
	Host       string          `json:"host"`
	Kind       string          `json:"kind"`
	Status     string          `json:"status"` // planned|pending_approval|running|done|failed|reverted|rejected
	Actor      Actor           `json:"actor"`
	Params     json.RawMessage `json:"params"`
	Plan       Plan            `json:"plan"`
	Undo       *UndoRecipe     `json:"undo,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	Log        string          `json:"log"`
	Reverts    string          `json:"reverts,omitempty"`
	RevertedBy string          `json:"reverted_by,omitempty"`
	TS         int64           `json:"ts"`
	FinishedAt int64           `json:"finished_at,omitempty"`
}

// Env is what a Kind's Plan and Apply see.
type Env struct {
	Ctx  context.Context
	Host string
	Log  func(format string, a ...any)
	Svc  any // the panel's services (*panel.App); set from Pipeline.Svc
	// set when applying (not planning)
	ChangeID  string
	ActorName string
	ActorIP   string // where the request came from (set when planning and applying)
	ActorType string // user | token | system … (set when applying)
	ActorID   string // the user's id for people
}

type Prop struct {
	Type string   `json:"type"` // string|boolean|integer|object|array
	Desc string   `json:"description,omitempty"`
	Enum []string `json:"enum,omitempty"`
}

type ParamSchema struct {
	Props    map[string]Prop `json:"properties"`
	Required []string        `json:"required,omitempty"`
}

// Kind describes one kind of change. The registry drives the API, MCP tools,
// CLI commands and equivalent-command strings.
type Kind struct {
	Name   string
	Perm   string // user permission key, e.g. containers.manage
	Scope  string // token scope, e.g. containers:write
	Title  string // i18n key
	Params ParamSchema
	CLI    string // text/template, e.g. "berth container restart {{.name}}"
	Plan   func(env Env, p json.RawMessage) (Plan, error)
	Apply  func(env Env, p json.RawMessage) (result any, undo *UndoRecipe, err error)
	Heavy  bool
	MemMB  int
	// MemFor, when set, works out the memory a particular run needs (MB) from its params.
	MemFor func(params json.RawMessage) int
	// Redact lists param names whose values never reach the audit log (password hashes,
	// secrets). Such kinds cannot wait for approval, because the stored params are incomplete.
	Redact []string
}

func newID() string { return "chg_" + sign.RandomHex(8) }
