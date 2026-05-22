// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package app — FastAPI → Go webservice port.
//
// Mirrors webservice/app.py:
//   - Chi router (replaces FastAPI)
//   - pongo2 for Jinja2 template compatibility (reuses all templates unchanged)
//   - /static mounted to bundled Bootstrap assets
//   - url_for(name, path=X) helper registered globally so templates render as-is
//   - Ensures DB schema before accepting requests
//   - Startup banner logged once
package app

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/db/seed"
	"github.com/mmt/mmt-studio-core/oam/banner"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/flosch/pongo2/v6"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Server ties the router, template set, and static-file handler together.
type Server struct {
	Router          *chi.Mux
	TemplateSet     *pongo2.TemplateSet
	TemplateDir     string
	StaticDir       string
	StaticURLPrefix string
	log             *logger.Logger

	httpServer *http.Server
	routesMu   sync.RWMutex
	routes     map[string]string // name → url path (for url_for)
}

// New builds a Server with templates + static rooted at the given dirs.
// Empty dirs fall back to ./webservice/templates and ./webservice/static.
func New(templateDir, staticDir string) (*Server, error) {
	if templateDir == "" {
		templateDir = filepath.Join("webservice", "templates")
	}
	if staticDir == "" {
		staticDir = filepath.Join("webservice", "static")
	}

	ts := pongo2.NewSet("mmt-core",
		pongo2.MustNewLocalFileSystemLoader(templateDir))

	s := &Server{
		Router:          chi.NewRouter(),
		TemplateSet:     ts,
		TemplateDir:     templateDir,
		StaticDir:       staticDir,
		StaticURLPrefix: "/static",
		log:             logger.Get("webservice"),
		routes:          make(map[string]string),
	}

	// Core middleware
	s.Router.Use(middleware.RequestID)
	s.Router.Use(middleware.RealIP)
	s.Router.Use(accessLog(s.log))
	s.Router.Use(middleware.Recoverer)

	// Static files at /static/... (Bootstrap bundled under webservice/static/bootstrap)
	fs := http.StripPrefix(s.StaticURLPrefix+"/", http.FileServer(http.Dir(staticDir)))
	s.Router.Handle(s.StaticURLPrefix+"/*", fs)
	s.Route("static", s.StaticURLPrefix)

	// Jinja2's url_for helper so HTML templates render unchanged:
	//   <link href="{{ url_for('static', path='bootstrap/css/bootstrap.min.css') }}">
	pongo2.RegisterFilter("url_for", nil) // keep registry healthy
	ts.Globals["url_for"] = func(name string, kwargs ...*pongo2.Value) string {
		return s.URLFor(name, kwargs...)
	}

	return s, nil
}

// Route records a named route so url_for can resolve it in templates.
func (s *Server) Route(name, path string) {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	s.routes[name] = path
}

// URLFor resolves a named route. The pongo2 signature receives positional
// pongo2.Value args; keyword args land as map[string]*pongo2.Value. We only
// use 'path' in the Python templates.
func (s *Server) URLFor(name string, kwargs ...*pongo2.Value) string {
	s.routesMu.RLock()
	base, ok := s.routes[name]
	s.routesMu.RUnlock()
	if !ok {
		return "/" + name
	}
	for _, kv := range kwargs {
		if kv == nil || kv.IsNil() {
			continue
		}
		// pongo2 passes kwargs as a dict that stringifies to "map[path:foo.css]"
		// in most templates — they call url_for('static', path='x.css'), which
		// pongo2 routes via reflection. The simplest reliable pattern is to
		// use url_for('static', 'foo.css') in templates OR detect a single
		// kwarg string value and append it.
		str := kv.String()
		if str != "" {
			return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(str, "/")
		}
	}
	return base
}

// Render serves an HTML response from a template + context.
func (s *Server) Render(w http.ResponseWriter, r *http.Request, name string, ctx pongo2.Context) {
	if ctx == nil {
		ctx = pongo2.Context{}
	}
	// Starlette-style "request" expected by some templates.
	ctx["request"] = r
	tmpl, err := s.TemplateSet.FromCache(name)
	if err != nil {
		s.log.Errorf("template %s: %v", name, err)
		http.Error(w, fmt.Sprintf("template error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteWriter(ctx, w); err != nil {
		s.log.Errorf("render %s: %v", name, err)
	}
}

// Listen binds the HTTP server. Blocks until Close or fatal error.
func (s *Server) Listen(addr string) error {
	s.log.Infof("webservice listening on %s", addr)
	s.httpServer = &http.Server{Addr: addr, Handler: s.Router}
	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully drains the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Bootstrap ensures the DB schema and prints the startup banner. Call once
// before Listen() — mirrors the top of webservice/app.py.
//
// Seed-on-cold-boot model: SeedAll is invoked ONLY when the DB file is
// absent at process start. A warm restart (process recycled by docker's
// restart policy after POST /api/admin/restart, or a normal container
// restart) finds the existing file and skips the seed entirely. The
// only paths that produce a freshly-seeded DB are:
//   1. First-ever start on a new host (no DB file yet).
//   2. POST /api/admin/remove-db-file (handler deletes the DB
//      file then exits; docker restart fires the cold-boot path).
//
// Why detect the file before EnsureSchema:
//   engine.Open() opens SQLite with a DSN that auto-creates the file
//   on first connect. So once EnsureSchema returns we can no longer
//   tell whether we were the creator. Stat early.
func (s *Server) Bootstrap() error {
	banner.Log()
	banner.CheckSysctls()

	coldBoot := false
	if _, err := os.Stat(engine.DBFilePath); err != nil && os.IsNotExist(err) {
		coldBoot = true
	}

	if err := engine.EnsureSchema(); err != nil {
		return fmt.Errorf("ensure_schema: %w", err)
	}

	if coldBoot {
		db, err := engine.Open()
		if err != nil {
			return fmt.Errorf("open db for seed: %w", err)
		}
		if err := seed.SeedAll(db); err != nil {
			return fmt.Errorf("seed_all: %w", err)
		}
		s.log.Infof("DB %s did not exist — cold-boot seed applied", engine.DBFilePath)
	} else {
		s.log.Infof("DB %s exists — warm boot, skipping seed", engine.DBFilePath)
	}
	return nil
}

// ── Middleware ──────────────────────────────────────────────────────────

func accessLog(log *logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Debugf("%s %s → %d (%d B)", r.Method, r.URL.Path, ww.Status(), ww.BytesWritten())
		})
	}
}

// OsExitOnFailure is a tiny convenience for main().
func OsExitOnFailure(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
