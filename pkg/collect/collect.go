// Package collect runs the live adapters: node discovery, per-endpoint API
// pollers and container log followers. Everything it learns is emitted as
// model.Observation on a single channel consumed by the state engine.
package collect

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"beescope/pkg/model"
	"beescope/pkg/source/beeapi"
	"beescope/pkg/source/logs"
	"beescope/pkg/source/metrics"
)

// Discoverer lists the nodes to observe.
type Discoverer interface {
	Discover(ctx context.Context) ([]model.NodeInfo, error)
}

// LogFollower opens a following log stream for a pod.
type LogFollower interface {
	FollowLogs(ctx context.Context, pod string, since time.Duration) (io.ReadCloser, error)
}

// Intervals configure polling frequency per endpoint. Zero disables one.
type Intervals struct {
	Discovery      time.Duration
	Health         time.Duration
	Status         time.Duration
	Topology       time.Duration
	Metrics        time.Duration
	Tags           time.Duration
	Stamps         time.Duration
	ChainState     time.Duration
	Redistribution time.Duration
}

// DefaultIntervals keep the load on Bee negligible: every call below is a
// cheap in-memory read except /redistributionstate (one chain call) and
// /chainstate (one RPC call), which are polled slowly.
func DefaultIntervals() Intervals {
	return Intervals{
		Discovery:      10 * time.Second,
		Health:         5 * time.Second,
		Status:         5 * time.Second,
		Topology:       5 * time.Second,
		Metrics:        5 * time.Second,
		Tags:           10 * time.Second,
		Stamps:         30 * time.Second,
		ChainState:     30 * time.Second,
		Redistribution: 60 * time.Second,
	}
}

// Collector owns the adapters.
type Collector struct {
	disc     Discoverer
	follower LogFollower // nil disables logs
	hc       *http.Client
	iv       Intervals
	out      chan<- model.Observation
	log      *slog.Logger
	logLines atomic.Uint64
	logDrops atomic.Uint64
	mu       sync.Mutex
	nodes    map[string]*worker
}

// New returns a collector.
func New(disc Discoverer, follower LogFollower, hc *http.Client, iv Intervals, out chan<- model.Observation, log *slog.Logger) *Collector {
	return &Collector{disc: disc, follower: follower, hc: hc, iv: iv, out: out, log: log, nodes: map[string]*worker{}}
}

// LogStats returns total log lines read and dropped.
func (c *Collector) LogStats() (lines, dropped uint64) {
	return c.logLines.Load(), c.logDrops.Load()
}

// Client returns an API client for a discovered node.
func (c *Collector) Client(node string) (*beeapi.Client, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	w, ok := c.nodes[node]
	if !ok {
		return nil, false
	}
	return beeapi.New(w.info.BaseURL, c.hc), true
}

type worker struct {
	info   model.NodeInfo
	cancel context.CancelFunc
	done   chan struct{}
}

// Run blocks until ctx is canceled; all goroutines it starts stop with it.
func (c *Collector) Run(ctx context.Context) {
	defer c.stopAll()
	t := time.NewTicker(c.iv.Discovery)
	defer t.Stop()
	for {
		c.discover(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) emit(ctx context.Context, o model.Observation) {
	select {
	case c.out <- o:
	case <-ctx.Done():
	}
}

func (c *Collector) discover(ctx context.Context) {
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	nodes, err := c.disc.Discover(dctx)
	if err != nil {
		c.emit(ctx, model.Observation{Kind: model.ObsPollError, Source: model.Source{Adapter: "kube", Ref: "discovery"}, ObservedAt: time.Now(), Err: err.Error()})
		return
	}
	seen := map[string]bool{}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range nodes {
		seen[n.Name] = true
		if w, ok := c.nodes[n.Name]; ok {
			if w.info.Pod == n.Pod {
				continue
			}
			// Pod replaced (restart, reschedule): restart the workers so
			// the log follower attaches to the new pod.
			w.cancel()
			<-w.done
		}
		info := n
		c.emit(ctx, model.Observation{Kind: model.ObsNodeSeen, Node: n.Name, Source: model.Source{Adapter: "kube", Ref: "discovery"}, ObservedAt: time.Now(), Info: &info})
		wctx, wcancel := context.WithCancel(ctx)
		w := &worker{info: n, cancel: wcancel, done: make(chan struct{})}
		c.nodes[n.Name] = w
		go c.runNode(wctx, w)
	}
	for name, w := range c.nodes {
		if seen[name] {
			continue
		}
		w.cancel()
		<-w.done
		delete(c.nodes, name)
		c.emit(ctx, model.Observation{Kind: model.ObsNodeGone, Node: name, Source: model.Source{Adapter: "kube", Ref: "discovery"}, ObservedAt: time.Now()})
	}
}

func (c *Collector) stopAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for name, w := range c.nodes {
		w.cancel()
		<-w.done
		delete(c.nodes, name)
	}
}

func (c *Collector) runNode(ctx context.Context, w *worker) {
	defer close(w.done)
	cl := beeapi.New(w.info.BaseURL, c.hc)
	var wg sync.WaitGroup
	poll := func(iv time.Duration, ref string, fn func(context.Context) model.Observation) {
		if iv <= 0 {
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.pollLoop(ctx, w.info.Name, iv, ref, fn)
		}()
	}
	poll(c.iv.Health, "GET /health", func(ctx context.Context) model.Observation {
		h, raw, err := cl.Health(ctx)
		return result(model.ObsHealth, err, raw, func(o *model.Observation) { o.Health = h })
	})
	poll(c.iv.Status, "GET /status", func(ctx context.Context) model.Observation {
		s, raw, err := cl.Status(ctx)
		return result(model.ObsStatus, err, raw, func(o *model.Observation) { o.Status = s })
	})
	poll(c.iv.Topology, "GET /topology", func(ctx context.Context) model.Observation {
		t, raw, err := cl.Topology(ctx)
		return result(model.ObsTopology, err, raw, func(o *model.Observation) { o.Topology = t })
	})
	poll(c.iv.Metrics, "GET /metrics", func(ctx context.Context) model.Observation {
		body, err := cl.Metrics(ctx)
		if err != nil {
			return result(model.ObsMetrics, err, nil, nil)
		}
		m, err := metrics.Parse(body)
		return result(model.ObsMetrics, err, nil, func(o *model.Observation) { o.Metrics = m })
	})
	poll(c.iv.Tags, "GET /tags", func(ctx context.Context) model.Observation {
		t, raw, err := cl.Tags(ctx)
		return result(model.ObsTags, err, raw, func(o *model.Observation) { o.Tags = t })
	})
	poll(c.iv.Stamps, "GET /stamps", func(ctx context.Context) model.Observation {
		s, raw, err := cl.Stamps(ctx)
		return result(model.ObsStamps, err, raw, func(o *model.Observation) { o.Stamps = s; o.Raw = raw })
	})
	poll(c.iv.ChainState, "GET /chainstate", func(ctx context.Context) model.Observation {
		s, raw, err := cl.ChainState(ctx)
		return result(model.ObsChainState, err, raw, func(o *model.Observation) { o.ChainState = s })
	})
	if w.info.Group != "light" {
		poll(c.iv.Redistribution, "GET /redistributionstate", func(ctx context.Context) model.Observation {
			r, raw, err := cl.Redistribution(ctx)
			return result(model.ObsRedistribution, err, raw, func(o *model.Observation) { o.Redistribution = r })
		})
	}
	if c.follower != nil && w.info.Pod != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.followLoop(ctx, w.info)
		}()
	}
	wg.Wait()
}

func result(kind model.ObsKind, err error, raw []byte, set func(*model.Observation)) model.Observation {
	if err != nil {
		return model.Observation{Kind: model.ObsPollError, Err: err.Error(), ErrCode: beeapi.Code(err), Raw: jsonOrNil(raw)}
	}
	o := model.Observation{Kind: kind, Raw: jsonOrNil(raw)}
	if set != nil {
		set(&o)
	}
	return o
}

func jsonOrNil(raw []byte) model.RawJSON {
	if len(raw) == 0 || len(raw) > 512<<10 {
		return nil
	}
	return raw
}

func (c *Collector) pollLoop(ctx context.Context, node string, iv time.Duration, ref string, fn func(context.Context) model.Observation) {
	timeout := min(iv, 10*time.Second)
	t := time.NewTicker(iv)
	defer t.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		start := time.Now()
		o := fn(rctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		o.Node = node
		o.Source = model.Source{Adapter: "api", Ref: ref}
		if o.Kind == model.ObsMetrics || (o.Kind == model.ObsPollError && ref == "GET /metrics") {
			o.Source.Adapter = "metrics"
		}
		o.ObservedAt = time.Now()
		o.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
		c.emit(ctx, o)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// followLoop tails a pod's log until ctx ends, reconnecting with backoff.
// Log lines are sent without blocking: when the engine falls behind, lines
// are dropped and the drop is reported, never silently lost.
func (c *Collector) followLoop(ctx context.Context, info model.NodeInfo) {
	since := 30 * time.Second
	backoff := time.Second
	var last time.Time
	for ctx.Err() == nil {
		rc, err := c.follower.FollowLogs(ctx, info.Pod, since)
		if err != nil {
			c.emit(ctx, model.Observation{Kind: model.ObsLogStream, Node: info.Name, Source: model.Source{Adapter: "log", Ref: "pod/" + info.Pod}, ObservedAt: time.Now(), Err: err.Error()})
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		c.emit(ctx, model.Observation{Kind: model.ObsLogStream, Node: info.Name, Source: model.Source{Adapter: "log", Ref: "pod/" + info.Pod}, ObservedAt: time.Now()})
		last = c.readLog(ctx, info, rc, last)
		_ = rc.Close()
		if !last.IsZero() {
			since = max(time.Since(last)+time.Second, time.Second)
		}
	}
}

func (c *Collector) readLog(ctx context.Context, info model.NodeInfo, rc io.Reader, after time.Time) time.Time {
	sc := bufio.NewScanner(rc)
	sc.Buffer(make([]byte, 64<<10), logs.MaxLine+1024)
	src := model.Source{Adapter: "log", Ref: "pod/" + info.Pod}
	dropped := 0
	flush := time.NewTicker(time.Second)
	defer flush.Stop()
	last := after
	for sc.Scan() {
		if ctx.Err() != nil {
			return last
		}
		kt, line := logs.SplitKubeTimestamp(sc.Text())
		if !kt.IsZero() && !after.IsZero() && !kt.After(after) {
			continue // already seen before the reconnect
		}
		if !kt.IsZero() {
			last = kt
		}
		c.logLines.Add(1)
		l, _ := logs.Parse(line)
		if !kt.IsZero() {
			l.Time = kt // kube timestamp is RFC3339 with zone; Bee's has none
		} else if l.Time.IsZero() {
			l.Time = time.Now()
		}
		o := model.Observation{Kind: model.ObsLog, Node: info.Name, Source: src, ObservedAt: l.Time, Log: &l}
		select {
		case c.out <- o:
		default:
			dropped++
			c.logDrops.Add(1)
		}
		select {
		case <-flush.C:
			if dropped > 0 {
				c.emit(ctx, model.Observation{Kind: model.ObsLogDropped, Node: info.Name, Source: src, ObservedAt: time.Now(), Dropped: dropped})
				dropped = 0
			}
		default:
		}
	}
	return last
}
