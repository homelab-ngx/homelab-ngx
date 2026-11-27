package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"homelab-ngx.com/core/internal/ask"
	"homelab-ngx.com/core/internal/audit"
	"homelab-ngx.com/core/internal/auth"
	"homelab-ngx.com/core/internal/backups"
	"homelab-ngx.com/core/internal/cmdrunner"
	"homelab-ngx.com/core/internal/config"
	"homelab-ngx.com/core/internal/drafts"
	"homelab-ngx.com/core/internal/hostcfg"
	"homelab-ngx.com/core/internal/httpx"
	"homelab-ngx.com/core/internal/installer"
	"homelab-ngx.com/core/internal/llm"
	"homelab-ngx.com/core/internal/logging"
	"homelab-ngx.com/core/internal/metrics"
	"homelab-ngx.com/core/internal/notifications"
	"homelab-ngx.com/core/internal/power"
	"homelab-ngx.com/core/internal/powermeter"
	"homelab-ngx.com/core/internal/powerprofile"
	"homelab-ngx.com/core/internal/services"
	"homelab-ngx.com/core/internal/stacks"
	"homelab-ngx.com/core/internal/storage"
	"homelab-ngx.com/core/internal/system"
	"homelab-ngx.com/core/internal/sysupdates"
	"homelab-ngx.com/core/internal/tilestatus"
)

// feature is one package's HTTP adapter: it registers its operations, each
// at its scope, on the operator API and, for read and control, the stack API.
type feature interface{ Register(httpx.APIs) }

// deps are what the adapters wrap. The OpenAPI command passes zero values:
// registering never touches them.
type deps struct {
	services      *services.Service
	backups       *backups.Service
	storage       *storage.Service
	stacks        *stacks.Service
	sysupdates    *sysupdates.Module
	notifications *notifications.Notifier
	channels      *notifications.Channels
	hostcfg       *hostcfg.Service
	config        *config.Manager
	runner        cmdrunner.Runner
	metrics       *metrics.Metrics
	drafts        *drafts.Service
	assistant     *llm.Settings
	ask           *ask.Service
	audit         *audit.Service
	installer     *installer.Service // nil: only the installer stick runs it
	auth          *auth.Service
	power         *power.Service
	powerProfile  *powerprofile.Service
	powerMeter    *powermeter.Service
}

func features(d deps) []feature {
	return []feature{
		services.NewHTTP(d.services),
		backups.NewHTTP(d.backups),
		storage.NewHTTP(d.storage),
		stacks.NewHTTP(d.stacks),
		sysupdates.NewHTTP(d.sysupdates),
		notifications.NewHTTP(d.notifications, d.channels),
		hostcfg.NewHTTP(d.hostcfg),
		system.NewHTTP(d.config, d.runner, logSources{stackUnits{d.stacks}, d.backups, d.hostcfg}),
		metrics.NewHTTP(d.metrics),
		drafts.NewHTTP(d.drafts),
		llm.NewHTTP(d.assistant),
		ask.NewHTTP(d.ask),
		audit.NewHTTP(d.audit),
		installer.NewHTTP(d.installer),
		auth.NewHTTP(d.auth),
		power.NewHTTP(d.power),
		powerprofile.NewHTTP(d.powerProfile),
		powermeter.NewHTTP(d.powerMeter),
		tilestatus.NewHTTP(tiles(d)),
	}
}

// tiles is each feature's reporter for the Settings grid's badges.
// A tile with no reporter (console, logs, account) has no badge. With the
// OpenAPI command's zero deps the reporters are never called.
func tiles(d deps) *tilestatus.Registry {
	g := tilestatus.NewRegistry(logging.Logger("tilestatus"))
	g.Add("hostcfg", d.hostcfg.Tiles)
	g.Add("notifications", notifications.Tiles(d.notifications, d.channels))
	g.Add("audit", d.audit.Tiles)
	g.Add("llm", d.assistant.Tiles)
	g.Add("sysupdates", d.sysupdates.Tiles)
	g.Add("power", d.power.Tiles)
	return g
}

// handlers builds the operator API, once as the CLI's socket serves it and
// once behind the dashboard's sign-in for the web socket, and the stack API
// (served on the stack listener), with every feature registered. The stack
// API checks each request's token, source address and scope before any
// operation runs.
func handlers(d deps) (operator, web, stack http.Handler) {
	version, _ := system.Running()
	opMux, stackMux := http.NewServeMux(), http.NewServeMux()
	apis := httpx.APIs{
		Operator: httpx.NewAPI(opMux, "homelab-daemon", version),
		Stack:    httpx.NewAPI(stackMux, "homelab-daemon for stacks", version),
	}
	apis.Stack.UseMiddleware(stacks.APIAuth(apis.Stack, d.stacks))
	for _, f := range features(d) {
		f.Register(apis)
	}
	// The stack editor's language server is a WebSocket, which Huma does
	// not describe: a plain handler on the operator API only.
	if d.drafts != nil {
		opMux.HandleFunc("GET "+httpx.Prefix+"/drafts/{name}/lsp", d.drafts.LSP)
	}
	// A stack shell is a WebSocket too, and the operator's only: root in
	// the container must never be reachable from the stack listener.
	if d.stacks != nil {
		opMux.HandleFunc("GET "+httpx.Prefix+"/stacks/{name}/shell", d.stacks.Shell)
	}
	return httpx.Middleware(nil)(opMux), httpx.Middleware(nil)(auth.Gate(d.auth)(opMux)), httpx.Middleware(nil)(stackMux)
}

// runOpenAPI prints an API's OpenAPI document: the operator API by default,
// the stack API with --stack. `make gen` commits the operator document as
// apps/core/api/openapi.json and builds the dashboard's types from it.
func runOpenAPI(args []string, w io.Writer) int {
	stack := len(args) == 1 && args[0] == "--stack"
	if len(args) > 1 || (len(args) == 1 && !stack) {
		fmt.Fprintln(w, "usage: homelab-daemon openapi [--stack]")
		return 2
	}
	version, _ := system.BuildInfo()
	apis := httpx.APIs{Operator: httpx.NewAPI(http.NewServeMux(), "homelab-daemon", version)}
	if stack {
		apis = httpx.APIs{
			Operator: httpx.NewAPI(http.NewServeMux(), "unused", version),
			Stack:    httpx.NewAPI(http.NewServeMux(), "homelab-daemon for stacks", version),
		}
	}
	for _, f := range features(deps{}) {
		f.Register(apis)
	}
	doc := apis.Operator.OpenAPI()
	if stack {
		doc = apis.Stack.OpenAPI()
	}
	// The version changes every commit; a fixed one keeps the committed
	// document stable.
	doc.Info.Version = "1"
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		fmt.Fprintln(w, "openapi:", err)
		return 1
	}
	return 0
}
