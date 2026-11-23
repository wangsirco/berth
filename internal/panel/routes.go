package panel

import (
	"crypto/subtle"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/wangsirco/berth/internal/sign"
)

const entryCookie = "berth_e"

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	// auth and setup
	mux.HandleFunc("GET /api/v1/meta", a.handleMeta)
	mux.HandleFunc("POST /api/v1/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", a.authed("", a.handleLogout))
	mux.HandleFunc("GET /api/v1/me", a.authed("", a.handleMe))
	mux.HandleFunc("GET /api/v1/setup/status", a.handleSetupStatus)
	mux.HandleFunc("POST /api/v1/setup", a.handleSetup)
	// users
	mux.HandleFunc("GET /api/v1/users", a.superOnly(a.handleUsers))
	mux.HandleFunc("POST /api/v1/users", a.superOnly(a.handleUserCreate))
	mux.HandleFunc("PATCH /api/v1/users/{id}", a.superOnly(a.handleUserUpdate))
	mux.HandleFunc("POST /api/v1/users/{id}/password", a.superOnly(a.handleUserPassword))
	mux.HandleFunc("GET /api/v1/permissions", a.authed("", a.handlePermissions))
	a.registerRoutes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/", a.handleStatic)
	a.apiMux = mux
	return a.securityHeaders(a.networkGate(a.entryGate(a.peerRoute(mux))))
}

// registerRoutes is where feature modules add their routes.
func (a *App) registerRoutes(mux *http.ServeMux) {
	for _, f := range routeHooks {
		f(a, mux)
	}
}

// routeHooks lets each feature file register its routes from init().
var routeHooks []func(a *App, mux *http.ServeMux)

func (a *App) securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; connect-src 'self' "+a.connectSrc()+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		hd.Set("Referrer-Policy", "no-referrer")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("Strict-Transport-Security", "max-age=31536000")
		h.ServeHTTP(w, r)
	})
}

// blank404 answers like any web server would, so the panel is not identifiable.
func blank404(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("404 page not found\n"))
}

func (a *App) networkGate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.allowed(r) && !remoteAgentPath(r.URL.Path) {
			blank404(w)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// remoteAgentPath: endpoints remote agents use from anywhere. Each authenticates on its
// own (one-time join code or a signed request) and answers a bare 404 otherwise.
func remoteAgentPath(p string) bool {
	// each of these proves itself (join codes, signed webhooks, migration-code bearer tokens)
	return strings.HasPrefix(p, "/join/") || strings.HasPrefix(p, "/link-api/") || strings.HasPrefix(p, "/hooks/pipelines/") || strings.HasPrefix(p, "/hooks/native/") || strings.HasPrefix(p, "/migrate/pull/") || strings.HasPrefix(p, "/status/") || strings.HasPrefix(p, "/sub/") || strings.HasPrefix(p, "/api/ext/wharf/") || p == "/api/v1/agents/enroll" || p == "/api/v1/agents/connect" || p == "/api/v1/agents/binary" || p == "/api/v1/ca.crt"
}

func (a *App) entryToken(entry string) string {
	salt := a.setting("access.entry_salt")
	if salt == "" {
		salt = sign.RandomHex(16)
		_ = a.setSetting("access.entry_salt", salt)
	}
	return tokenHash("entry:" + entry + ":" + salt)
}

// entryGate hides the panel behind the secret entry path (方案 §7.5) once one is set.
func (a *App) entryGate(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entry := a.setting("access.entry")
		// before the first administrator exists the one-time setup code is the secret,
		// so the setup link works even when the installer already set an entry
		if entry == "" || a.userCount() == 0 {
			h.ServeHTTP(w, r)
			return
		}
		want := a.entryToken(entry)
		if r.URL.Path == entry {
			setCookie(w, entryCookie, want, 30*24*3600)
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		if c, err := r.Cookie(entryCookie); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(want)) == 1 {
			h.ServeHTTP(w, r)
			return
		}
		// Tokens, signed-in sessions and the CA download do not need the entry cookie.
		// invitation, file-share, desktop-share and proxy-subscription links check their own codes
		// …and Wharf's pairing callback, which checks the one-time code this panel handed out
		if strings.HasPrefix(r.URL.Path, "/invite/") || strings.HasPrefix(r.URL.Path, "/s/") || strings.HasPrefix(r.URL.Path, "/v/") || strings.HasPrefix(r.URL.Path, "/sub/") || r.URL.Path == "/wharf/paired" || strings.HasPrefix(r.URL.Path, "/hop/") || r.URL.Path == "/sso/wharf" {
			h.ServeHTTP(w, r)
			return
		}
		if hasBearer(r) || r.URL.Path == "/api/v1/ca.crt" || r.URL.Path == "/mcp" || remoteAgentPath(r.URL.Path) {
			h.ServeHTTP(w, r)
			return
		}
		if s := a.loadSession(r); s != nil {
			h.ServeHTTP(w, r)
			return
		}
		blank404(w)
	})
}

func (a *App) handleStatic(w http.ResponseWriter, r *http.Request) {
	if a.web == nil {
		blank404(w)
		return
	}
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if st, err := fs.Stat(a.web, p); err != nil || st.IsDir() {
		p = "index.html" // client-side routes
	}
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, a.web, p)
}
