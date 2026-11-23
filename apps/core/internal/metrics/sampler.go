// Package metrics samples the host and every running stack every 2 s, keeps
// the last 15 minutes in memory for the live charts, and stores a row per
// scope per minute in its own database (metrics.db) for 30 days.
package metrics

import (
	"context"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"homelab-ngx.com/core/internal/services"
)

const (
	// Every is how often the sampler reads.
	Every = 2 * time.Second
	// liveSpan is how much the in-memory ring holds: the Live chart.
	liveSpan = 15 * time.Minute
	// scopesTTL bounds how often the stacks are listed.
	scopesTTL = 30 * time.Second

	hostScope = "@host"
)

// The parts of the host that are not stacks. With the stacks they add up to
// the host.
const (
	partServices = iota // system.slice (the daemon among them) and init.scope
	partSessions        // user.slice: login sessions and their user services
	partKernel          // the rest: work and memory no cgroup is charged for
	nPart
)

// partScopes are the parts' scope names in metrics.db. Any other scope
// starting with "@" is from an older layout and is skipped.
var partScopes = [nPart]string{"@services", "@sessions", "@kernel"}

// partFigs are the figures that add up: CPU, memory and disk. Pressure is a
// share of time, not a sum, and the network of the stacks partly never
// leaves the host, so the parts have neither.
var partFigs = []int{figCPU, figMemory, figDiskRead, figDiskWrite}

// The figures of a point, in the order of the metrics.db columns.
const (
	figCPU = iota
	figMemory
	figPressureCPU
	figPressureMemory
	figPressureIO
	figDiskRead
	figDiskWrite
	figNetRx
	figNetTx
	figTempCPU
	figTempDisk
	figCPUMax
	figMemoryMax
	nFig
)

// point is one scope's figures at one moment; NaN is no reading.
type point [nFig]float64

func emptyPoint() point {
	var p point
	for i := range p {
		p[i] = nan
	}
	return p
}

// sample is every scope at one moment.
type sample struct {
	at     time.Time
	host   point
	parts  [nPart]point
	stacks map[string]point
}

func emptyParts() [nPart]point {
	var ps [nPart]point
	for i := range ps {
		ps[i] = emptyPoint()
	}
	return ps
}

// Scope is a stack the sampler reads: its cgroup is
// <Machines>/<Name>.scope, Iface is the host side of its veth, and Units
// are the services whose CPU and memory the Stacks table shows.
type Scope struct {
	Name  string
	Iface string // the host side of its veth
	Units []string
}

// Stacks lists the installed stacks. The sampler reads only those whose
// cgroup exists, so a stopped stack costs a stat.
type Stacks interface {
	Scopes(ctx context.Context) []Scope
}

// reading is the counters of one key and when they were read.
type reading struct {
	at time.Time
	c  counters
}

// Metrics owns the sampler, the live ring and metrics.db.
type Metrics struct {
	paths  Paths
	stacks Stacks
	db     *DB
	now    func() time.Time
	log    *slog.Logger

	cores    int
	memTotal float64
	bootedAt int64
	model    string
	cpu      string
	disks    disks
	sensors  sensors

	// Touched by the sampling goroutine only.
	prev     map[string]reading
	scopes   []Scope
	scopesAt time.Time
	minute   minuteAcc
	prunedAt time.Time

	mu    sync.Mutex
	ring  []sample
	usage map[string]services.Usage // per stack and "<stack>/<unit>"
	subs  map[chan sample]struct{}
}

// New wires the sampler. stacks and db may be nil.
func New(paths Paths, stacks Stacks, db *DB, log *slog.Logger) *Metrics {
	if log == nil {
		log = slog.Default()
	}
	m := &Metrics{
		paths: paths, stacks: stacks, db: db, now: time.Now, log: log,
		cores: cpuCount(paths.Proc), bootedAt: bootTime(paths.Proc), model: machineModel(paths.DMI), cpu: cpuName(paths.Proc),
		disks: disks{devBlock: paths.DevBlock}, sensors: findSensors(paths.Hwmon),
		prev: map[string]reading{}, usage: map[string]services.Usage{}, subs: map[chan sample]struct{}{},
	}
	_, m.memTotal, _ = hostMemory(paths.Proc)
	return m
}

// Info returns the host's fixed facts.
func (m *Metrics) Info() HostInfo {
	return HostInfo{Cores: m.cores, MemoryTotal: int64(m.memTotal), BootedAt: m.bootedAt, Model: m.model, CPU: m.cpu}
}

// Run samples until ctx ends, then stores the minute in progress.
func (m *Metrics) Run(ctx context.Context) {
	m.seed(ctx)
	t := time.NewTicker(Every)
	defer t.Stop()
	for {
		m.tick(ctx)
		select {
		case <-ctx.Done():
			m.flushMinute(context.WithoutCancel(ctx))
			return
		case <-t.C:
		}
	}
}

// Usage is the last CPU (top-style: 100 is one core) and memory of a stack,
// or of "<stack>/<unit>"; nil until two readings exist.
func (m *Metrics) Usage(key string) *services.Usage {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.usage[key]
	if !ok {
		return nil
	}
	return &u
}

// Subscribe returns the live ring as frames and a channel that receives
// every later sample, until cancel is called.
func (m *Metrics) Subscribe() (snapshot MetricFrames, samples <-chan sample, cancel func()) {
	ch := make(chan sample, 8)
	m.mu.Lock()
	snapshot = framesOf(m.ring, int(Every.Seconds()), false)
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	return snapshot, ch, func() {
		m.mu.Lock()
		delete(m.subs, ch)
		m.mu.Unlock()
	}
}

// currentScopes lists the stacks, at most every scopesTTL.
func (m *Metrics) currentScopes(ctx context.Context) []Scope {
	if m.stacks == nil {
		return nil
	}
	if now := m.now(); now.Sub(m.scopesAt) >= scopesTTL || m.scopesAt.IsZero() {
		m.scopes, m.scopesAt = m.stacks.Scopes(ctx), now
	}
	return m.scopes
}

// tick reads every source once, turns the counters into rates, and hands
// the sample to the ring, the subscribers and the minute.
func (m *Metrics) tick(ctx context.Context) {
	now := m.now()
	next := map[string]reading{}
	usage := map[string]services.Usage{}

	host, hostOK := m.rate(next, hostScope, now, m.readHost())
	s := sample{at: now, host: host, parts: emptyParts(), stacks: map[string]point{}}
	if hostOK {
		s.host[figTempCPU] = readTemp(m.sensors.cpu)
		s.host[figTempDisk] = readTemp(m.sensors.disk)
	}

	for _, sc := range m.currentScopes(ctx) {
		dir := filepath.Join(m.paths.Machines, sc.Name+".scope")
		if _, err := os.Stat(dir); err != nil {
			continue // not running
		}
		c, ok := m.readCgroup(dir, true)
		if !ok {
			continue
		}
		c.netRx, c.netTx, c.hasNet = vethNet(m.paths.Net, sc.Iface)
		if p, ok := m.rate(next, sc.Name, now, c); ok {
			s.stacks[sc.Name] = p
			usage[sc.Name] = services.Usage{CpuPercent: float32(p[figCPU] * float64(m.cores)), MemoryBytes: int64(c.memory)}
		}
		for _, u := range sc.Units {
			key := sc.Name + "/" + u
			uc, ok := m.readCgroup(filepath.Join(dir, "payload", "system.slice", u), false)
			if !ok {
				continue
			}
			if p, ok := m.rate(next, key, now, uc); ok {
				usage[key] = services.Usage{CpuPercent: float32(p[figCPU] * float64(m.cores)), MemoryBytes: int64(uc.memory)}
			}
		}
	}
	s.parts = m.readParts(next, now)
	m.prev = next

	m.mu.Lock()
	m.usage = usage
	m.mu.Unlock()
	if !hostOK {
		return // the first reading: no rates yet
	}
	s.parts[partKernel] = kernelOf(s.host, s.stacks, s.parts)

	m.mu.Lock()
	m.ring = append(m.ring, s)
	cut := 0
	for cut < len(m.ring) && now.Sub(m.ring[cut].at) > liveSpan {
		cut++
	}
	m.ring = m.ring[cut:]
	for ch := range m.subs {
		select {
		case ch <- s:
		default: // a slow reader misses a point rather than stalling the sampler
		}
	}
	m.mu.Unlock()

	m.addToMinute(ctx, s)
}

// readHost reads the whole machine: the root cgroup's CPU, pressure and
// disks, /proc/meminfo and the physical interfaces.
func (m *Metrics) readHost() counters {
	var c counters
	c.cpuUsec, _ = readCPU(m.paths.Cgroup)
	c.psi = readPressure(m.paths.Cgroup)
	c.diskRead, c.diskW = m.disks.readIO(m.paths.Cgroup)
	c.memory, _, _ = hostMemory(m.paths.Proc)
	c.netRx, c.netTx = hostNet(m.paths.Proc, m.paths.Net)
	c.hasNet = true
	return c
}

// readCgroup reads a cgroup's CPU and memory, and with full its pressure
// and disk traffic too.
func (m *Metrics) readCgroup(dir string, full bool) (counters, bool) {
	var c counters
	var ok bool
	if c.cpuUsec, ok = readCPU(dir); !ok {
		return c, false
	}
	if c.memory, ok = cgroupMemory(dir); !ok {
		return c, false
	}
	if full {
		c.psi = readPressure(dir)
		c.diskRead, c.diskW = m.disks.readIO(dir)
	}
	return c, true
}

// rate records key's reading in next and returns the rates since its
// previous one; false when there is none, or a counter went back (the
// cgroup was recreated).
func (m *Metrics) rate(next map[string]reading, key string, now time.Time, c counters) (point, bool) {
	next[key] = reading{at: now, c: c}
	prev, had := m.prev[key]
	p := emptyPoint()
	if !had {
		return p, false
	}
	dt := now.Sub(prev.at).Seconds()
	pc := prev.c
	if dt <= 0 || c.cpuUsec < pc.cpuUsec || c.diskRead < pc.diskRead || c.diskW < pc.diskW {
		return p, false
	}
	usec := dt * 1e6
	p[figCPU] = clamp(float64(c.cpuUsec-pc.cpuUsec)/(usec*float64(m.cores))*100, 100)
	p[figMemory] = c.memory
	for i, f := range []int{figPressureCPU, figPressureMemory, figPressureIO} {
		if c.psi[i] >= pc.psi[i] {
			p[f] = clamp(float64(c.psi[i]-pc.psi[i])/usec*100, 100)
		}
	}
	p[figDiskRead] = float64(c.diskRead-pc.diskRead) / dt
	p[figDiskWrite] = float64(c.diskW-pc.diskW) / dt
	if c.hasNet && pc.hasNet && c.netRx >= pc.netRx && c.netTx >= pc.netTx {
		p[figNetRx] = float64(c.netRx-pc.netRx) / dt
		p[figNetTx] = float64(c.netTx-pc.netTx) / dt
	}
	return p, true
}

func clamp(v, hi float64) float64 { return math.Max(0, math.Min(v, hi)) }

// readParts reads the parts of the host that are cgroups and records their
// counters in next. The kernel's part is left empty: it is what remains once
// the stacks are read too (kernelOf). A cgroup that cannot be read counts as
// nothing.
func (m *Metrics) readParts(next map[string]reading, now time.Time) [nPart]point {
	read := func(key, dir string) point {
		if dir == "" {
			return emptyPoint()
		}
		c, ok := m.readCgroup(dir, true)
		if !ok {
			return emptyPoint()
		}
		p, _ := m.rate(next, key, now, c)
		return p
	}
	services := read("@cgroup/services", m.paths.Services)
	init := read("@cgroup/init", m.paths.Init)
	sessions := read("@cgroup/sessions", m.paths.Sessions)

	ps := emptyParts()
	for _, f := range partFigs {
		if !math.IsNaN(services[f]) {
			ps[partServices][f] = services[f] + zeroNaN(init[f])
		}
		ps[partSessions][f] = sessions[f]
	}
	return ps
}

// kernelOf is the host less every stack and every other part, for the
// figures that add up: the kernel's own work and memory, such as the file
// system's caches, and anything no cgroup is charged for.
func kernelOf(host point, stacks map[string]point, parts [nPart]point) point {
	k := emptyPoint()
	for _, f := range partFigs {
		v := host[f]
		for _, p := range stacks {
			v -= zeroNaN(p[f])
		}
		for i, p := range parts {
			if i != partKernel {
				v -= zeroNaN(p[f])
			}
		}
		k[f] = math.Max(0, v)
	}
	return k
}

func zeroNaN(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return v
}

// framesOf turns samples into columns. withMax adds the max columns
// (history rows carry them; live samples do not).
func framesOf(ss []sample, step int, withMax bool) MetricFrames {
	f := MetricFrames{At: make([]int64, len(ss)), Step: step, Stacks: map[string]MetricSeries{}}
	names := map[string]bool{}
	for _, s := range ss {
		for n := range s.stacks {
			names[n] = true
		}
	}
	hosts := make([]point, len(ss))
	var parts [nPart][]point
	for i := range parts {
		parts[i] = make([]point, len(ss))
	}
	stacks := map[string][]point{}
	for n := range names {
		stacks[n] = make([]point, len(ss))
	}
	for i, s := range ss {
		f.At[i] = s.at.Unix()
		hosts[i] = s.host
		for j := range parts {
			parts[j][i] = s.parts[j]
		}
		for n := range names {
			if p, ok := s.stacks[n]; ok {
				stacks[n][i] = p
			} else {
				stacks[n][i] = emptyPoint()
			}
		}
	}
	f.Host = seriesOf(hosts, withMax)
	f.Services, f.Sessions, f.Kernel = seriesOf(parts[partServices], withMax), seriesOf(parts[partSessions], withMax), seriesOf(parts[partKernel], withMax)
	for n, ps := range stacks {
		f.Stacks[n] = seriesOf(ps, withMax)
	}
	f.TempCPU, f.TempDisk = column(hosts, figTempCPU), column(hosts, figTempDisk)
	return f
}

func column(ps []point, fig int) []Num {
	out := make([]Num, len(ps))
	for i, p := range ps {
		out[i] = Num(p[fig])
	}
	return out
}

func seriesOf(ps []point, withMax bool) MetricSeries {
	s := MetricSeries{
		CPU: column(ps, figCPU), Memory: column(ps, figMemory),
		PressureCPU: column(ps, figPressureCPU), PressureMemory: column(ps, figPressureMemory), PressureIO: column(ps, figPressureIO),
		DiskRead: column(ps, figDiskRead), DiskWrite: column(ps, figDiskWrite),
		NetRx: column(ps, figNetRx), NetTx: column(ps, figNetTx),
	}
	if withMax {
		s.CPUMax, s.MemoryMax = column(ps, figCPUMax), column(ps, figMemoryMax)
	}
	return s
}

// minuteAcc averages the samples of one minute per scope.
type minuteAcc struct {
	start  time.Time
	scopes map[string]*acc
}

type acc struct {
	sum [nFig]float64
	n   [nFig]int
	max [2]float64 // cpu, memory
}

func (a *acc) add(p point) {
	for i, v := range p {
		if !math.IsNaN(v) {
			a.sum[i] += v
			a.n[i]++
		}
	}
	if !math.IsNaN(p[figCPU]) {
		a.max[0] = math.Max(a.max[0], p[figCPU])
	}
	if !math.IsNaN(p[figMemory]) {
		a.max[1] = math.Max(a.max[1], p[figMemory])
	}
}

func (a *acc) point() point {
	p := emptyPoint()
	for i := range p {
		if a.n[i] > 0 {
			p[i] = a.sum[i] / float64(a.n[i])
		}
	}
	if a.n[figCPU] > 0 {
		p[figCPUMax] = a.max[0]
	}
	if a.n[figMemory] > 0 {
		p[figMemoryMax] = a.max[1]
	}
	return p
}

// addToMinute adds a sample to the minute it falls in, storing the minute
// before when s starts a new one, and prunes old rows once an hour.
func (m *Metrics) addToMinute(ctx context.Context, s sample) {
	start := s.at.Truncate(time.Minute)
	if !m.minute.start.Equal(start) {
		m.flushMinute(ctx)
		m.minute = minuteAcc{start: start, scopes: map[string]*acc{}}
	}
	add := func(scope string, p point) {
		a := m.minute.scopes[scope]
		if a == nil {
			a = &acc{}
			m.minute.scopes[scope] = a
		}
		a.add(p)
	}
	add(hostScope, s.host)
	for i, p := range s.parts {
		add(partScopes[i], p)
	}
	for n, p := range s.stacks {
		add(n, p)
	}
	if m.db != nil && s.at.Sub(m.prunedAt) >= time.Hour {
		m.prunedAt = s.at
		if err := m.db.prune(ctx, s.at.Add(-Retention)); err != nil {
			m.log.Warn("metrics: prune", "error", err)
		}
	}
}

// flushMinute stores the minute in progress, if any.
func (m *Metrics) flushMinute(ctx context.Context) {
	if m.db == nil || len(m.minute.scopes) == 0 {
		return
	}
	rows := make(map[string]point, len(m.minute.scopes))
	for scope, a := range m.minute.scopes {
		rows[scope] = a.point()
	}
	if err := m.db.write(ctx, m.minute.start, rows); err != nil {
		m.log.Warn("metrics: store minute", "error", err)
	}
	m.minute.scopes = nil
}

// seed fills the ring from the stored minutes of the live span, so the
// Live chart is not empty after a restart.
func (m *Metrics) seed(ctx context.Context) {
	if m.db == nil {
		return
	}
	now := m.now()
	ss, err := m.db.samples(ctx, now.Add(-liveSpan), 60)
	if err != nil {
		m.log.Warn("metrics: seed the live ring", "error", err)
		return
	}
	sort.Slice(ss, func(i, j int) bool { return ss[i].at.Before(ss[j].at) })
	m.mu.Lock()
	m.ring = append(ss, m.ring...)
	m.mu.Unlock()
}
