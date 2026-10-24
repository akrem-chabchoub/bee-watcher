// Command bee-scope is a real-time visual debugger for a local Bee cluster.
//
//	bee-scope                      # observe the k3d-bee cluster, UI on http://127.0.0.1:7070
//	bee-scope --sim                # simulated cluster, no Kubernetes needed
//	bee-scope --record s.ndjson    # also record every observation
//	bee-scope --replay s.ndjson    # investigate a recording
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"beescope/pkg/collect"
	"beescope/pkg/control"
	"beescope/pkg/engine"
	"beescope/pkg/history"
	"beescope/pkg/model"
	"beescope/pkg/server"
	"beescope/pkg/source/kube"
	"beescope/pkg/source/otlp"
	"beescope/pkg/source/record"
	"beescope/pkg/source/sim"
	"beescope/web"
)

var version = "dev"

type options struct {
	kubeContext, namespace, kubeconfig string
	allowRemote                        bool
	listen, otlpListen                 string
	sim                                bool
	record, replay                     string
	speed                              float64
	noLogs                             bool
	intervals                          string
	history                            time.Duration
	dev                                bool
	devURL                             string
	verbose                            bool
	showVersion                        bool
	open                               bool
}

func main() {
	var o options
	flag.StringVar(&o.kubeContext, "context", "k3d-bee", "kube context of the local k3d cluster")
	flag.StringVar(&o.namespace, "namespace", "local", "namespace of the Bee nodes")
	flag.StringVar(&o.kubeconfig, "kubeconfig", "", "kubeconfig path (default: standard loading rules)")
	flag.BoolVar(&o.allowRemote, "allow-remote", false, "allow a context that is not k3d-* on a loopback API server (dangerous)")
	flag.StringVar(&o.listen, "listen", "127.0.0.1:7070", "UI and API listen address")
	flag.StringVar(&o.otlpListen, "otlp-listen", "127.0.0.1:4318", "OTLP/HTTP trace receiver address (empty disables)")
	flag.BoolVar(&o.sim, "sim", false, "run against a simulated cluster (everything is labeled simulated)")
	flag.StringVar(&o.record, "record", "", "append every observation to this NDJSON file")
	flag.StringVar(&o.replay, "replay", "", "replay observations from this NDJSON file instead of a cluster")
	flag.Float64Var(&o.speed, "speed", 1, "replay / simulation speed multiplier (replay: 0 = as fast as possible)")
	flag.BoolVar(&o.noLogs, "no-logs", false, "do not follow pod logs")
	flag.StringVar(&o.intervals, "intervals", "", "poll interval overrides, e.g. health=5s,metrics=10s (keys: discovery, health, status, topology, metrics, tags, stamps, chainstate, redistribution; 0 disables)")
	flag.DurationVar(&o.history, "history", 30*time.Minute, "how much history to keep in memory")
	flag.BoolVar(&o.dev, "dev", false, "proxy the UI to the Vite dev server (see --dev-url)")
	flag.StringVar(&o.devURL, "dev-url", "http://127.0.0.1:5173", "Vite dev server used with --dev")
	flag.BoolVar(&o.verbose, "v", false, "verbose logging")
	flag.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	flag.BoolVar(&o.open, "open", false, "open the UI in the default browser once ready")
	flag.Parse()
	if o.showVersion {
		fmt.Println("bee-scope", version)
		return
	}
	level := slog.LevelInfo
	if o.verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	if err := run(o, log); err != nil {
		fmt.Fprintln(os.Stderr, "bee-scope:", err)
		os.Exit(1)
	}
}

func run(o options, log *slog.Logger) error {
	if o.sim && o.replay != "" {
		return errors.New("--sim and --replay are exclusive")
	}
	if o.record != "" && o.replay != "" {
		return errors.New("--record and --replay are exclusive")
	}
	iv := collect.DefaultIntervals()
	if err := parseIntervals(o.intervals, &iv); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	obs := make(chan model.Observation, 16384)
	var engIn <-chan model.Observation = obs
	if o.record != "" {
		f, err := os.OpenFile(o.record, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return fmt.Errorf("open recording: %w", err)
		}
		defer f.Close()
		teeOut := make(chan model.Observation, 16384)
		go func() {
			if err := record.Tee(ctx, obs, teeOut, f); err != nil {
				log.Error("recording stopped", "error", err)
			}
		}()
		engIn = teeOut
		log.Info("recording observations", "file", o.record)
	}

	ecfg := engine.Config{Intervals: iv, Log: log}
	meta := server.Meta{Version: version, StartedAt: time.Now(), Logs: !o.noLogs, HistoryS: o.history.Seconds(), Intervals: intervalMap(iv)}
	var nodes server.NodeAPI
	var ctl *control.Controller
	var eng *engine.Engine
	var start []func()

	switch {
	case o.sim:
		s := sim.New(sim.Options{Speed: o.speed, Intervals: iv, Seed: uint64(time.Now().UnixNano())})
		ecfg.Mode, ecfg.Target, ecfg.LogStats = "sim", "simulated cluster (1 bootnode, 7 bees)", s.LogStats
		start = append(start, func() { s.Run(ctx, obs) })
		meta.Logs = true
	case o.replay != "":
		if _, err := os.Stat(o.replay); err != nil {
			return fmt.Errorf("replay: %w", err)
		}
		clock := &record.Clock{}
		ecfg.Mode, ecfg.Target, ecfg.Clock = "replay", "replay of "+filepath.Base(o.replay), clock.Now
		start = append(start, func() {
			if err := record.Replay(ctx, o.replay, o.speed, obs, clock); err != nil {
				log.Error("replay failed", "error", err)
			}
		})
	default:
		kc, err := kube.New(kube.Options{Kubeconfig: o.kubeconfig, Context: o.kubeContext, Namespace: o.namespace, AllowRemote: o.allowRemote})
		if err != nil {
			return err
		}
		var follower collect.LogFollower = kc
		if o.noLogs {
			follower = nil
		}
		col := collect.New(kc, follower, kc.HTTPClient(), iv, obs, log)
		ecfg.Mode, ecfg.Target, ecfg.LogStats = "live", kc.Context+"/"+o.namespace, col.LogStats
		nodes = col
		meta.Actions = true
		start = append(start, func() { col.Run(ctx) })
		if err := kc.Err(); err != nil {
			log.Warn("cluster not reachable yet; open the Cluster tab to create or start it", "context", kc.Context, "error", err)
		} else {
			log.Info("observing cluster", "context", kc.Context, "server", kc.Server, "namespace", o.namespace)
		}
		stateDir, err := os.UserConfigDir()
		if err != nil {
			stateDir = os.TempDir()
		}
		ctl = control.New(control.Config{Kube: kc, KubeContext: kc.Context, Namespace: o.namespace, StateDir: filepath.Join(stateDir, "bee-scope"), Log: log,
			OnJob: func(v control.JobView) {
				if eng == nil {
					return
				}
				sev, summary := model.SevInfo, "cluster job started: "+v.Title
				switch v.State {
				case control.JobSucceeded:
					summary = "cluster job succeeded: " + v.Title
				case control.JobFailed:
					sev, summary = model.SevWarn, "cluster job failed: "+v.Title+": "+v.Error
				case control.JobCanceled:
					sev, summary = model.SevWarn, "cluster job canceled: "+v.Title
				}
				_ = eng.Record(context.WithoutCancel(ctx), model.Event{Kind: "control.job", Severity: sev, Provenance: model.Observed,
					Source: model.Source{Adapter: "bee-scope", Ref: "job " + v.ID + " " + v.Action}, Summary: summary})
			}})
		ctl.SetOnTarget(func(k, ns string) {
			if eng != nil {
				_ = eng.SetTarget(context.WithoutCancel(ctx), k+"/"+ns)
			}
			log.Info("switched cluster", "context", k, "namespace", ns)
		})
		// Reopen the cluster chosen in the UI last time, unless --context or
		// --namespace was given explicitly.
		if st := ctl.Settings(); st.KubeContext != "" && !flagSet("context") && !flagSet("namespace") && (st.KubeContext != kc.Context || st.Namespace != o.namespace) {
			ns := st.Namespace
			if ns == "" {
				ns = o.namespace
			}
			if err := ctl.SetTarget(st.KubeContext, ns); err != nil {
				log.Warn("could not reopen the last cluster; staying on the default", "context", st.KubeContext, "error", err)
			} else {
				ecfg.Target = st.KubeContext + "/" + ns
			}
		}
		start = append(start, func() { ctl.DefaultsFromDeployment(ctx) })
		if o.otlpListen != "" {
			ecfg.OTLP = o.otlpListen
			otlpSrv := &http.Server{Addr: o.otlpListen, Handler: otlpMux(obs), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				if err := otlpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("OTLP receiver stopped", "addr", o.otlpListen, "error", err)
				}
			}()
			go func() { <-ctx.Done(); _ = otlpSrv.Close() }()
			log.Info("OTLP trace receiver", "addr", o.otlpListen, "path", "/v1/traces")
		}
	}
	meta.Mode, meta.Target, meta.OTLP = ecfg.Mode, ecfg.Target, ecfg.OTLP

	lim := history.DefaultLimits()
	lim.MaxAge = o.history
	lim.MaxKeyframes = max(int(o.history/(5*time.Second))+10, 10)
	hist := history.New(lim)
	hub := engine.NewHub()
	eng = engine.New(ecfg, engIn, hist, hub)
	go eng.Run(ctx)
	for _, f := range start {
		go f()
	}

	ui, err := fs.Sub(web.Dist, "dist")
	if err != nil {
		return fmt.Errorf("embedded UI: %w", err)
	}
	if _, err := fs.Stat(ui, "assets"); err != nil && !o.dev {
		log.Warn("the embedded UI is not built; run `make web` and rebuild (the API works without it)")
	}
	scfg := server.Config{Engine: eng, Hist: hist, Hub: hub, Meta: meta, Nodes: nodes, Control: ctl, UI: ui, Log: log, Quit: stop}
	if o.dev {
		scfg.Dev = o.devURL
	}
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		if o.open && errors.Is(err, syscall.EADDRINUSE) {
			// Launched again from the app bundle: bee-scope is most likely
			// already running, so just show it.
			log.Info("address in use; opening the running bee-scope", "addr", o.listen)
			openBrowser("http://" + o.listen)
			return nil
		}
		return fmt.Errorf("listen: %w", err)
	}
	if host, _, _ := net.SplitHostPort(o.listen); !isLoopback(host) {
		log.Warn("listening on a non-loopback address: bee-scope has no authentication and exposes cluster details and actions", "addr", o.listen)
	}
	srv := &http.Server{
		Handler:           server.New(scfg),
		ReadHeaderTimeout: 10 * time.Second,
		// Request contexts end with ctx, so open SSE streams close on
		// shutdown instead of holding it up.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	mode := meta.Mode
	if mode == "sim" {
		mode = "SIMULATION"
	}
	uiURL := "http://" + ln.Addr().String()
	log.Info("bee-scope ready", "url", uiURL, "mode", mode, "version", version)
	if o.open {
		openBrowser(uiURL)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func otlpMux(out chan<- model.Observation) http.Handler {
	m := http.NewServeMux()
	m.Handle("/v1/traces", otlp.Handler(out))
	return m
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func parseIntervals(s string, iv *collect.Intervals) error {
	if s == "" {
		return nil
	}
	fields := map[string]*time.Duration{
		"discovery": &iv.Discovery, "health": &iv.Health, "status": &iv.Status, "topology": &iv.Topology, "metrics": &iv.Metrics,
		"tags": &iv.Tags, "stamps": &iv.Stamps, "chainstate": &iv.ChainState, "redistribution": &iv.Redistribution,
	}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(kv), "=")
		if !ok {
			return fmt.Errorf("--intervals: %q is not key=duration", kv)
		}
		p, ok := fields[strings.ToLower(k)]
		if !ok {
			return fmt.Errorf("--intervals: unknown key %q", k)
		}
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return fmt.Errorf("--intervals: bad duration %q for %s", v, k)
		}
		if k == "discovery" && d == 0 {
			return errors.New("--intervals: discovery cannot be disabled")
		}
		if d > 0 && d < time.Second {
			return fmt.Errorf("--intervals: %s=%s is below the 1s minimum", k, d)
		}
		if strings.EqualFold(k, "redistribution") && d > 0 && d < 30*time.Second {
			return errors.New("--intervals: redistribution makes a chain call on the node; keep it at 30s or more")
		}
		*p = d
	}
	return nil
}

func intervalMap(iv collect.Intervals) map[string]float64 {
	return map[string]float64{
		"discovery": iv.Discovery.Seconds(), "health": iv.Health.Seconds(), "status": iv.Status.Seconds(), "topology": iv.Topology.Seconds(),
		"metrics": iv.Metrics.Seconds(), "tags": iv.Tags.Seconds(), "stamps": iv.Stamps.Seconds(), "chainState": iv.ChainState.Seconds(),
		"redistribution": iv.Redistribution.Seconds(),
	}
}

func openBrowser(u string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	_ = cmd.Start()
}

// flagSet reports whether a flag was given on the command line.
func flagSet(name string) bool {
	set := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			set = true
		}
	})
	return set
}
