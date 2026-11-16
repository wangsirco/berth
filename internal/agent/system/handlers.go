package system

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wangsirco/berth/internal/agent"
	"github.com/wangsirco/berth/internal/agent/docker"
	"github.com/wangsirco/berth/internal/errs"
)

func fail(w http.ResponseWriter, err error) {
	if e, ok := err.(*errs.E); ok { // already carries its own message key
		agent.WriteErr(w, e)
		return
	}
	msg := err.Error()
	if why, ok := strings.CutPrefix(msg, "protected:"); ok {
		agent.WriteErr(w, errs.New(409, "system.protected", "why", why))
		return
	}
	// "refuse:<code>" carries its own message key, system.<code>
	if code, ok := strings.CutPrefix(msg, "refuse:"); ok {
		agent.WriteErr(w, errs.New(409, "system."+code))
		return
	}
	agent.WriteErr(w, errs.New(400, "system.failed", "detail", msg))
}

func reply(w http.ResponseWriter, v any, err error) {
	if err != nil {
		fail(w, err)
		return
	}
	if v == nil {
		v = map[string]bool{"ok": true}
	}
	agent.WriteJSON(w, 200, v)
}

// Register adds /v1/system/*.
func Register(s *agent.Server, dc *docker.Client) {
	go RestoreNftTables(context.Background())
	s.Handle("GET /v1/system/info", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, GetInfo(r.Context()))
	})
	s.Handle("GET /v1/system/timezones", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, Timezones(r.Context()))
	})
	s.Handle("POST /v1/system/hostname", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Name string }
		json.Unmarshal(body, &p)
		reply(w, nil, SetHostname(r.Context(), p.Name))
	})
	s.Handle("POST /v1/system/timezone", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Timezone string }
		json.Unmarshal(body, &p)
		reply(w, nil, SetTimezone(r.Context(), p.Timezone))
	})
	s.Handle("POST /v1/system/ntp", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ On bool }
		json.Unmarshal(body, &p)
		reply(w, nil, SetNTP(r.Context(), p.On))
	})
	s.Handle("POST /v1/system/power", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Action  string
			Minutes int
		}
		json.Unmarshal(body, &p)
		if p.Action == "cancel" {
			CancelPower(r.Context())
			reply(w, nil, nil)
			return
		}
		reply(w, nil, SchedulePower(r.Context(), p.Action, p.Minutes))
	})

	// network
	s.Handle("GET /v1/system/nics", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, err := NICs(r.Context())
		cp, at := Pending()
		reply(w, map[string]any{"items": list, "routes": Routes(r.Context()), "dns": Resolvers(r.Context()),
			"pending": cp != "", "rollback_at": at, "nm": has("nmcli")}, err)
	})
	s.Handle("POST /v1/system/nics/{name}", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			NICSpec
			Seconds int `json:"seconds"`
		}
		json.Unmarshal(body, &p)
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		err := ApplyNIC(r.Context(), r.PathValue("name"), p.NICSpec, p.Seconds, log)
		if err == nil {
			_, at := Pending()
			b, _ := json.Marshal(map[string]any{"rollback_at": at})
			log("::result " + string(b))
		} else {
			err = errs.New(400, "system.net_failed", "detail", err.Error())
		}
		st.End(err)
	})
	s.Handle("POST /v1/system/net/confirm", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, ConfirmNet(r.Context()))
	})
	s.Handle("POST /v1/system/net/rollback", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, RollbackNet(r.Context()))
	})

	// services
	s.Handle("GET /v1/system/services", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, err := Services(r.Context())
		reply(w, map[string]any{"items": list}, err)
	})
	s.Handle("POST /v1/system/services/{name}/{action}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, ServiceAction(r.Context(), r.PathValue("name"), r.PathValue("action")))
	})
	s.Handle("GET /v1/system/services/{name}/logs", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		n, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		out, err := ServiceLogs(r.Context(), r.PathValue("name"), n)
		reply(w, map[string]string{"text": out}, err)
	})

	// processes
	s.Handle("GET /v1/system/processes", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list := Processes(r.Context(), 300)
		names := containerNames(r.Context(), dc)
		for i := range list {
			if id := list[i].Container; id != "" {
				for full, name := range names {
					if strings.HasPrefix(full, id) {
						list[i].Container = name
					}
				}
			}
		}
		agent.WriteJSON(w, 200, map[string]any{"items": list})
	})
	s.Handle("POST /v1/system/processes/{pid}/signal", func(w http.ResponseWriter, r *http.Request, body []byte) {
		pid, _ := strconv.Atoi(r.PathValue("pid"))
		var p struct{ Signal string }
		json.Unmarshal(body, &p)
		reply(w, nil, Signal(pid, p.Signal))
	})

	// disks
	s.Handle("GET /v1/system/disks", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, err := Disks(r.Context())
		reply(w, map[string]any{"items": list}, err)
	})

	// scheduled jobs: the system's own (read-only) and running one of Berth's
	s.Handle("GET /v1/system/cron", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"entries": Crontabs(), "timers": Timers(r.Context())})
	})
	s.Handle("POST /v1/system/cron/exec", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Script  string `json:"script"`
			User    string `json:"user"`
			Timeout int    `json:"timeout"` // seconds
		}
		json.Unmarshal(body, &p)
		st := agent.NewStream(w)
		start := time.Now()
		code, err := ExecJob(r.Context(), p.Script, p.User, time.Duration(p.Timeout)*time.Second, func(l string) { st.Send(map[string]string{"l": l}) })
		res := map[string]any{"code": code, "ms": time.Since(start).Milliseconds()}
		if err != nil {
			res["error"] = err.Error()
		}
		b, _ := json.Marshal(res)
		st.Send(map[string]string{"l": "::result " + string(b)})
		st.End(nil)
	})

	// SSH server and keys
	s.Handle("GET /v1/system/ssh", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, GetSSH(r.Context()))
	})
	s.Handle("POST /v1/system/ssh", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			SSHSettings
			Seconds int `json:"seconds"`
		}
		json.Unmarshal(body, &p)
		reply(w, nil, SetSSH(r.Context(), p.SSHSettings, p.Seconds))
	})
	s.Handle("POST /v1/system/ssh/confirm", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, ConfirmSSH(r.Context()))
	})
	s.Handle("POST /v1/system/ssh/rollback", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, nil, RollbackSSH(r.Context()))
	})
	s.Handle("POST /v1/system/ssh/keys", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ User, Add, Remove string }
		json.Unmarshal(body, &p)
		fp, line, err := ModifyKeys(p.User, p.Add, p.Remove)
		reply(w, map[string]string{"fingerprint": fp, "removed": line}, err)
	})

	// users and groups
	s.Handle("GET /v1/system/accounts", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, Accounts())
	})
	s.Handle("POST /v1/system/accounts", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var op AccountOp
		json.Unmarshal(body, &op)
		reply(w, nil, ApplyAccount(r.Context(), op))
	})

	// kernel, memory, resolver, automatic updates
	s.Handle("GET /v1/system/tuning", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"sysctl": GetSysctl(), "swap": GetSwap(r.Context()), "resolver": GetResolver(r.Context()), "auto_update": GetAutoUpdate(r.Context())})
	})
	s.Handle("POST /v1/system/sysctl", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ File, Apply map[string]string }
		json.Unmarshal(body, &p)
		reply(w, nil, SetSysctl(r.Context(), p.File, p.Apply))
	})
	s.Handle("POST /v1/system/swap", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Zram   *string `json:"zram"`
			FileMB *int    `json:"file_mb"`
		}
		json.Unmarshal(body, &p)
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		var err error
		if p.FileMB != nil {
			err = SetSwapFile(r.Context(), *p.FileMB, log)
		}
		if err == nil && p.Zram != nil {
			err = SetZram(r.Context(), *p.Zram, log)
		}
		if err != nil {
			err = errs.New(400, "system.failed", "detail", err.Error())
		}
		st.End(err)
	})
	s.Handle("POST /v1/system/resolver", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Servers []string }
		json.Unmarshal(body, &p)
		reply(w, nil, SetResolver(r.Context(), p.Servers))
	})
	s.Handle("POST /v1/system/auto-update", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p AutoUpdate
		json.Unmarshal(body, &p)
		st := agent.NewStream(w)
		err := SetAutoUpdate(r.Context(), p, func(l string) { st.Send(map[string]string{"l": l}) })
		if err != nil {
			msg := err.Error()
			if code, ok := strings.CutPrefix(msg, "refuse:"); ok {
				err = errs.New(409, "system."+code)
			} else {
				err = errs.New(400, "system.failed", "detail", msg)
			}
		}
		st.End(err)
	})

	// storage: disks, mounts, RAID, space, clean-up
	streamOp := func(w http.ResponseWriter, fn func(log func(string)) (any, error)) {
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		res, err := fn(log)
		if err != nil {
			msg := err.Error()
			if code, ok := strings.CutPrefix(msg, "refuse:"); ok {
				err = errs.New(409, "system."+code)
			} else {
				err = errs.New(400, "system.failed", "detail", msg)
			}
		} else if res != nil {
			b, _ := json.Marshal(res)
			log("::result " + string(b))
		}
		st.End(err)
	}
	s.Handle("GET /v1/system/storage", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st, err := GetStorage(r.Context())
		reply(w, st, err)
	})
	s.Handle("POST /v1/system/storage/format", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Device, FSType, Label string }
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) { return nil, Format(r.Context(), p.Device, p.FSType, p.Label, log) })
	})
	s.Handle("POST /v1/system/storage/mount", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Device, Path, Options string
			Persist               bool
		}
		json.Unmarshal(body, &p)
		reply(w, nil, Mount(r.Context(), p.Device, p.Path, p.Options, p.Persist))
	})
	s.Handle("POST /v1/system/storage/unmount", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Path    string
			Persist bool
		}
		json.Unmarshal(body, &p)
		reply(w, nil, Unmount(r.Context(), p.Path, p.Persist))
	})
	s.Handle("POST /v1/system/storage/raid", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Op, Kind, Name, Level, Array, Device, Target string
			Devices                                      []string
		}
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) {
			switch p.Op {
			case "create":
				path, err := CreateRAID(r.Context(), p.Kind, p.Name, p.Level, p.Devices, log)
				return map[string]string{"path": path}, err
			case "add", "remove":
				return nil, RepairRAID(r.Context(), p.Array, p.Op, p.Device, log)
			case "scrub":
				return nil, Scrub(r.Context(), p.Target, log)
			case "delete":
				return nil, DeleteRAID(r.Context(), p.Array, log)
			}
			return nil, errors.New("unknown RAID operation")
		})
	})
	s.Handle("GET /v1/system/space", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		path := r.URL.Query().Get("path")
		if path == "" {
			path = "/"
		}
		rep, err := Space(r.Context(), path, r.URL.Query().Get("refresh") == "1")
		reply(w, rep, err)
	})
	s.Handle("GET /v1/system/cleanup", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		var reclaim func(context.Context) (int64, int64)
		if dc != nil {
			reclaim = dc.Reclaimable
		}
		agent.WriteJSON(w, 200, map[string]any{"items": CleanupSuggestions(r.Context(), reclaim)})
	})
	s.Handle("POST /v1/system/cleanup", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Key string }
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) {
			var freed int64
			var err error
			switch p.Key {
			case "images", "buildcache":
				if dc == nil {
					return nil, errors.New("Docker is not available")
				}
				if p.Key == "images" {
					freed, err = dc.PruneImages(r.Context())
				} else {
					freed, err = dc.PruneBuildCache(r.Context())
				}
				log(fmt.Sprintf("freed %d bytes", freed))
			default:
				err = RunCleanup(r.Context(), p.Key, log)
			}
			return map[string]int64{"freed": freed}, err
		})
	})

	// projects kept running by systemd
	s.Handle("POST /v1/system/projects", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p ProjectSpec
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) { return nil, SetProject(r.Context(), p, log) })
	})
	s.Handle("POST /v1/system/projects/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Name string }
		json.Unmarshal(body, &p)
		reply(w, nil, RemoveProject(r.Context(), p.Name))
	})

	// runtimes installed by mise
	s.Handle("GET /v1/system/runtimes", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		list, has := Runtimes(r.Context())
		agent.WriteJSON(w, 200, map[string]any{"items": list, "mise": has})
	})
	s.Handle("POST /v1/system/runtimes", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Tool    string `json:"tool"`
			Version string `json:"version"`
			Remove  bool   `json:"remove"`
		}
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) {
			return nil, SetRuntime(r.Context(), p.Tool, p.Version, p.Remove, log)
		})
	})

	// SFTP sub-accounts
	s.Handle("GET /v1/system/sftp", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, map[string]any{"items": ListSFTP()})
	})
	s.Handle("POST /v1/system/sftp", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p SFTPAccount
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) { return nil, SetSFTP(r.Context(), p, log) })
	})
	s.Handle("POST /v1/system/sftp/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct{ Name string }
		json.Unmarshal(body, &p)
		streamOp(w, func(log func(string)) (any, error) { return nil, RemoveSFTP(r.Context(), p.Name) })
	})

	// native projects (R1)
	s.Handle("POST /v1/native/check", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Spec NativeSpec }
		json.Unmarshal(body, &in)
		rep, err := NativeCheck(r.Context(), in.Spec)
		reply(w, rep, err)
	})
	s.Handle("GET /v1/native/{name}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		st, err := NativeStat(r.Context(), r.PathValue("name"))
		reply(w, st, err)
	})
	nativeStream := func(w http.ResponseWriter, fn func(log func(string)) (any, error)) {
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		res, err := fn(log)
		if err == nil && res != nil {
			b, _ := json.Marshal(res)
			log("::result " + string(b))
		}
		if err != nil {
			if _, ok := err.(*errs.E); !ok {
				err = errs.New(500, "native.failed", "detail", err.Error())
			}
		}
		st.End(err)
	}
	s.Handle("POST /v1/native/deploy", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Spec NativeSpec
			Env  map[string]string
		}
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) { return NativeDeploy(r.Context(), in.Spec, in.Env, log) })
	})
	s.Handle("POST /v1/native/activate", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Spec    NativeSpec
			Release string
		}
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) { return NativeActivate(r.Context(), in.Spec, in.Release, log) })
	})
	s.Handle("POST /v1/native/retire", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Name string
			Port int
		}
		json.Unmarshal(body, &in)
		reply(w, nil, NativeRetire(r.Context(), in.Name, in.Port))
	})
	s.Handle("POST /v1/native/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Name string }
		json.Unmarshal(body, &in)
		reply(w, nil, NativeRemove(r.Context(), in.Name))
	})

	// native PHP (R2)
	s.Handle("POST /v1/native/workers", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Spec NativeSpec }
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) { return nil, NativeSyncWorkers(r.Context(), in.Spec, log) })
	})
	s.Handle("GET /v1/dbnative", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, map[string]any{"items": DBNatives(r.Context())}, nil)
	})
	s.Handle("GET /v1/dbnative/{engine}/log", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, map[string]any{"lines": DBLogTail(r.PathValue("engine"), 3000)}, nil)
	})
	s.Handle("GET /v1/dbnative/{engine}/live", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, map[string]any{"values": DBLive(r.Context(), r.PathValue("engine"))}, nil)
	})
	s.Handle("POST /v1/dbnative/install", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Engine, Version, Password string }
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) {
			return DBNativeInstall(r.Context(), in.Engine, in.Version, in.Password, log)
		})
	})
	s.Handle("POST /v1/dbnative/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Engine string }
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) { return nil, DBNativeRemove(r.Context(), in.Engine, log) })
	})
	s.Handle("POST /v1/dbnative/tune", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Engine string
			MemMB  int `json:"mem_mb"`
			Values map[string]string
		}
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) {
			return nil, DBTune(r.Context(), in.Engine, in.MemMB, in.Values, log)
		})
	})
	s.Handle("POST /v1/dbnative/allow", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Engine  string
			Sources []string
		}
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, DBAllow(r.Context(), in.Engine, in.Sources))
	})
	s.Handle("POST /v1/dbnative/password", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Engine, User, Password string }
		json.Unmarshal(body, &in)
		reply(w, map[string]bool{"ok": true}, DBNativePassword(r.Context(), in.Engine, in.User, in.Password))
	})
	s.Handle("GET /v1/phpnative", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		reply(w, map[string]any{"items": PHPNativeRuntimes(r.Context())}, nil)
	})
	s.Handle("POST /v1/phpnative/install", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			Version    string
			Extensions []string
			Remove     bool
		}
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) {
			if in.Remove {
				return nil, PHPUninstall(r.Context(), in.Version, log)
			}
			return nil, PHPInstall(r.Context(), in.Version, in.Extensions, log)
		})
	})
	s.Handle("POST /v1/phpsite/set", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in PHPSite
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) { return PHPSiteSet(r.Context(), in, log) })
	})
	s.Handle("POST /v1/phpsite/remove", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			ID     int64
			Domain string
			Owner  string
		}
		json.Unmarshal(body, &in)
		reply(w, nil, PHPSiteRemove(r.Context(), in.ID, in.Domain, in.Owner))
	})
	s.Handle("GET /v1/phpsite/{id}", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		reply(w, PHPSiteStat(r.Context(), id), nil)
	})
	s.Handle("POST /v1/phpsite/exec", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct {
			ID      int64
			Domain  string
			Version string
			Command string
		}
		json.Unmarshal(body, &in)
		nativeStream(w, func(log func(string)) (any, error) {
			return nil, PHPSiteExec(r.Context(), in.ID, in.Domain, in.Version, in.Command, log)
		})
	})
	s.Handle("POST /v1/phpsite/check", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var in struct{ Domain string }
		json.Unmarshal(body, &in)
		if !domainDirRe.MatchString(in.Domain) || strings.Contains(in.Domain, "..") {
			reply(w, nil, errs.New(400, "php.bad_site"))
			return
		}
		reply(w, CheckPHPDir(filepath.Join(PHPSitesRoot, in.Domain)), nil)
	})

	// packages
	s.Handle("GET /v1/system/packages", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.URL.Query().Get("user") == "1" {
			l, err := UserPackages(r.Context())
			reply(w, map[string]any{"items": l}, err)
			return
		}
		l, err := SearchPackages(r.Context(), r.URL.Query().Get("q"))
		reply(w, map[string]any{"items": l}, err)
	})
	s.Handle("POST /v1/system/packages/dry", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Names  []string `json:"names"`
			Remove bool     `json:"remove"`
		}
		json.Unmarshal(body, &p)
		d, err := DryRunPackages(r.Context(), p.Names, p.Remove)
		reply(w, d, err)
	})
	s.Handle("POST /v1/system/packages", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Names  []string `json:"names"`
			Remove bool     `json:"remove"`
		}
		json.Unmarshal(body, &p)
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		d, err := ApplyPackages(r.Context(), p.Names, p.Remove, log)
		agent.ForgetCaps() // docker, compose, nft… may have come or gone
		if err == nil {
			b, _ := json.Marshal(d)
			log("::result " + string(b))
		}
		st.End(err)
	})

	// btrfs system snapshots
	s.Handle("GET /v1/system/snapshots", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, SnapshotStatus(r.Context()))
	})
	s.Handle("POST /v1/system/snapshots", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			Reason string `json:"reason"`
			Auto   bool   `json:"auto"`
		}
		json.Unmarshal(body, &p)
		sn, err := SnapshotCreate(r.Context(), p.Reason, p.Auto)
		reply(w, sn, err)
	})
	s.Handle("POST /v1/system/snapshots/{op}", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		json.Unmarshal(body, &p)
		var err error
		switch r.PathValue("op") {
		case "delete":
			err = SnapshotDelete(r.Context(), p.ID)
		case "delete-previous":
			err = SnapshotDeletePrevious(r.Context(), p.Name)
		case "rollback":
			err = SnapshotRollback(r.Context(), p.ID)
		case "undo":
			err = SnapshotUndoRollback(r.Context())
		default:
			err = errs.New(404, "snap.bad_op")
		}
		reply(w, map[string]bool{"ok": err == nil}, err)
	})

	// updates
	s.Handle("GET /v1/system/updates", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		agent.WriteJSON(w, 200, CheckUpdates(r.Context(), r.URL.Query().Get("refresh") == "1"))
	})
	s.Handle("POST /v1/system/updates", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var p struct {
			SecurityOnly bool `json:"security_only"`
		}
		json.Unmarshal(body, &p)
		st := agent.NewStream(w)
		log := func(l string) { st.Send(map[string]string{"l": l}) }
		err := ApplyUpdates(r.Context(), p.SecurityOnly, log)
		if err != nil {
			err = errs.New(500, "system.update_failed", "detail", err.Error())
		} else {
			b, _ := json.Marshal(GetInfo(context.Background()))
			log("::result " + string(b))
		}
		st.End(err)
	})
}

func containerNames(ctx context.Context, dc *docker.Client) map[string]string {
	out := map[string]string{}
	if dc == nil {
		return out
	}
	cs, err := dc.Containers(ctx)
	if err != nil {
		return out
	}
	for _, c := range cs {
		out[c.ID] = c.Name
	}
	return out
}
