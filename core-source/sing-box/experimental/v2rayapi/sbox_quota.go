// SBOX accounting extension. GPL-3.0-or-later, same license as sing-box.
// Only local authorization and managed-identity connection lifecycle are added.
package v2rayapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

func sboxPath(key, fallback string) string {
	if p := os.Getenv(key); p != "" {
		return p
	}
	return fallback
}
func sboxRootFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	owner, ok := st.Sys().(*syscall.Stat_t)
	if !st.Mode().IsRegular() || !ok || owner.Uid != 0 || st.Mode().Perm()&0007 != 0 || st.Mode().Perm()&0022 != 0 || st.Size() > 1048576 {
		return nil, errors.New("unsafe accounting control file")
	}
	return io.ReadAll(io.LimitReader(f, 1048577))
}
func sboxAuth(listen string) (grpc.UnaryServerInterceptor, error) {
	host, _, err := net.SplitHostPort(listen)
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		return nil, errors.New("SBOX StatsService requires an explicit loopback address")
	}
	key, err := sboxRootFile(sboxPath("SBOX_STATS_TOKEN_FILE", "/etc/sbox-manager/stats-auth.key"))
	if err != nil {
		return nil, errors.New("SBOX StatsService authorization file is unavailable")
	}
	if len(key) < 32 || len(key) > 256 {
		return nil, errors.New("invalid StatsService authorization key")
	}
	expected := "Bearer " + string(key)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		values := metadata.ValueFromIncomingContext(ctx, "authorization")
		if len(values) != 1 || subtle.ConstantTimeCompare([]byte(values[0]), []byte(expected)) != 1 {
			return nil, status.Error(codes.Unauthenticated, "local StatsService authorization required")
		}
		switch v := req.(type) {
		case *QueryStatsRequest:
			if v.Reset_ {
				return nil, status.Error(codes.PermissionDenied, "counter resets are forbidden")
			}
		case *GetStatsRequest:
			if v.Reset_ {
				return nil, status.Error(codes.PermissionDenied, "counter resets are forbidden")
			}
		case *SysStatsRequest:
		default:
			return nil, status.Error(codes.PermissionDenied, "read-only StatsService")
		}
		return next(ctx, req)
	}, nil
}

var (
	sboxManaged   = regexp.MustCompile(`^user-[a-f0-9]{32}(?:@(reality|vmess|hy2|tuic|anytls))?$`)
	sboxLimitUser = regexp.MustCompile(`^user-[a-f0-9]{32}$`)
	errQuota      = errors.New("managed payload budget exhausted")
)

// A ceiling is absolute for this process; -1 means unlimited. Every flow
// of an identity shares one ledger. Pending writes count against available
// credit, but never appear in StatsService until the write finishes.
type sboxBudget struct {
	mu                      sync.Mutex
	ceiling, spent, pending int64
}

func (b *sboxBudget) reserve(size int, datagram bool) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := int64(size)
	if b.ceiling >= 0 {
		available := max(int64(0), b.ceiling-b.spent-b.pending)
		if datagram && n > available {
			return 0
		}
		n = min(n, available)
	}
	b.pending += n
	return int(n)
}
func (b *sboxBudget) finish(reserved, transferred int, counter *atomic.Int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.pending -= int64(reserved)
	b.spent += int64(transferred)
	if counter != nil {
		counter.Add(int64(transferred))
	}
}
func (b *sboxBudget) exhausted() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ceiling >= 0 && b.spent >= b.ceiling
}
func (b *sboxBudget) setCeiling(n int64) {
	b.mu.Lock()
	b.ceiling = n
	b.mu.Unlock()
}

type sboxGuard struct {
	mu       sync.Mutex
	blocked  map[string]bool
	budgets  map[string]*sboxBudget
	live     map[io.Closer]string
	instance string
	revision int64
	ready    bool
	last     []byte
	stop     chan struct{}
	once     sync.Once
}

func sboxInstance() string {
	boot, e := os.ReadFile("/proc/sys/kernel/random/boot_id")
	stat, f := os.ReadFile("/proc/self/stat")
	if e != nil || f != nil {
		return ""
	}
	end := strings.LastIndexByte(string(stat), ')')
	if end < 0 {
		return ""
	}
	fields := strings.Fields(string(stat[end+1:]))
	if len(fields) < 20 {
		return ""
	}
	return strings.TrimSpace(string(boot)) + ":" + strconv.Itoa(os.Getpid()) + ":" + fields[19]
}
func newSboxGuard() *sboxGuard {
	g := &sboxGuard{blocked: map[string]bool{}, budgets: map[string]*sboxBudget{}, live: map[io.Closer]string{}, instance: sboxInstance(), stop: make(chan struct{})}
	g.load()
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-g.stop:
				return
			case <-tick.C:
				g.load()
				g.closeExhausted()
			}
		}
	}()
	return g
}
func sboxUserOf(tag string) string {
	if i := strings.IndexByte(tag, '@'); i > 0 {
		return tag[:i]
	}
	return tag
}
func (g *sboxGuard) load() {
	data, err := sboxRootFile(sboxPath("SBOX_QUOTA_POLICY_FILE", "/etc/sbox-manager/quota-runtime.json"))
	if err != nil || bytes.Equal(g.last, data) {
		return
	}
	var policy struct {
		Blocked  []string         `json:"blocked"`
		Limits   map[string]int64 `json:"limits"`
		Instance string           `json:"core_instance"`
		Revision int64            `json:"revision"`
	}
	if json.Unmarshal(data, &policy) != nil || policy.Blocked == nil || len(policy.Blocked) > 5000 || len(policy.Limits) > 1000 {
		return
	}
	// Once a collector has published into this process, an older snapshot
	// or an out-of-order publication cannot re-credit a live account.
	if g.revision > 0 && (policy.Instance != g.instance || policy.Revision <= g.revision) {
		return
	}
	if policy.Limits == nil {
		policy.Limits = make(map[string]int64)
	}
	next := map[string]bool{}
	for _, tag := range policy.Blocked {
		if !sboxManaged.MatchString(tag) {
			return
		}
		next[tag] = true
		// Manager-issued block lists apply to the entire account. Retain a zero
		// data-plane allowance while closing existing sockets.
		policy.Limits[sboxUserOf(tag)] = 0
	}
	for user, n := range policy.Limits {
		if !sboxLimitUser.MatchString(user) || n < 0 {
			return
		}
		// Policies from an earlier process (including pre-upgrade snapshots)
		// carry absolute counters in that process's coordinate system.
		if g.instance == "" || policy.Instance != g.instance {
			policy.Limits[user] = 0
		}
	}
	g.mu.Lock()
	g.blocked = next
	g.ready = true
	if policy.Instance == g.instance {
		g.revision = policy.Revision
	}
	for id, b := range g.budgets {
		n, ok := policy.Limits[id]
		if !ok {
			n = -1
		}
		b.setCeiling(n)
	}
	for id, n := range policy.Limits {
		if g.budgets[id] == nil {
			g.budgets[id] = &sboxBudget{ceiling: n}
		}
	}
	g.last = data
	g.mu.Unlock()
}
func (g *sboxGuard) closeExhausted() {
	g.mu.Lock()
	var list []io.Closer
	for c, tag := range g.live {
		id := sboxUserOf(tag)
		if g.blocked[tag] || g.blocked[id] || g.budgets[id].exhausted() {
			list = append(list, c)
		}
	}
	g.mu.Unlock()
	for _, c := range list {
		c.Close()
	}
}
func (g *sboxGuard) add(c io.Closer, tag string) (*sboxBudget, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id := sboxUserOf(tag)
	b := g.budgets[id]
	if b == nil {
		b = &sboxBudget{ceiling: -1}
		g.budgets[id] = b
	}
	if !g.ready || g.blocked[tag] || g.blocked[id] || b.exhausted() || len(g.live) >= 8192 {
		return b, false
	}
	g.live[c] = tag
	return b, true
}
func (g *sboxGuard) remove(c io.Closer) { g.mu.Lock(); delete(g.live, c); g.mu.Unlock() }
func (g *sboxGuard) close()             { g.once.Do(func() { close(g.stop) }) }

type sboxMeter struct {
	budget   *sboxBudget
	up, down *atomic.Int64
}

func (s *StatsService) meter(tag string) sboxMeter {
	var m sboxMeter
	if s.users[tag] {
		s.access.Lock()
		m.up = s.loadOrCreateCounter("user>>>" + tag + ">>>traffic>>>uplink")
		m.down = s.loadOrCreateCounter("user>>>" + tag + ">>>traffic>>>downlink")
		s.access.Unlock()
	}
	return m
}

type sboxConn struct {
	net.Conn
	sboxMeter
	g    *sboxGuard
	once sync.Once
}

func (c *sboxConn) Read(p []byte) (int, error) {
	if len(p) > 0 && c.budget.exhausted() {
		return 0, errQuota
	}
	n, err := c.Conn.Read(p)
	if n == 0 {
		return n, err
	}
	// Do not hold credit across a blocking read. Charge decrypted bytes
	// before returning them to the router; unreadable suffixes never forward.
	allowed := c.budget.reserve(n, false)
	c.budget.finish(allowed, allowed, c.up)
	if allowed == 0 {
		return 0, errQuota
	}
	return allowed, err
}
func (c *sboxConn) Write(p []byte) (int, error) {
	n := c.budget.reserve(len(p), false)
	if n == 0 && len(p) > 0 {
		return 0, errQuota
	}
	written, err := c.Conn.Write(p[:n])
	c.budget.finish(n, written, c.down)
	if err == nil && written < len(p) {
		err = io.ErrShortWrite
	}
	return written, err
}
func (c *sboxConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.g.remove(c) })
	return err
}

type sboxPacket struct {
	N.PacketConn
	sboxMeter
	g    *sboxGuard
	once sync.Once
}

func (c *sboxPacket) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	if c.budget.exhausted() {
		return M.Socksaddr{}, errQuota
	}
	start := b.Len()
	dst, err := c.PacketConn.ReadPacket(b)
	if err != nil {
		return dst, err
	}
	size := b.Len() - start
	n := c.budget.reserve(size, true)
	if n != size {
		b.Truncate(start)
		return M.Socksaddr{}, errQuota
	}
	c.budget.finish(n, n, c.up)
	return dst, nil
}
func (c *sboxPacket) WritePacket(b *buf.Buffer, dst M.Socksaddr) error {
	size := b.Len()
	n := c.budget.reserve(size, true)
	if n != size || size == 0 && c.budget.exhausted() {
		b.Release()
		return errQuota
	}
	err := c.PacketConn.WritePacket(b, dst)
	written := n
	if err != nil {
		written = 0
	}
	c.budget.finish(n, written, c.down)
	return err
}
func (c *sboxPacket) Close() error {
	err := c.PacketConn.Close()
	c.once.Do(func() { c.g.remove(c) })
	return err
}
func (s *StatsService) sboxConnection(conn net.Conn, user string) net.Conn {
	if !sboxManaged.MatchString(user) {
		return conn
	}
	c := &sboxConn{Conn: conn, g: s.guard, sboxMeter: s.meter(user)}
	b, ok := s.guard.add(c, user)
	c.budget = b
	if !ok {
		c.Close()
		c.budget = &sboxBudget{ceiling: 0}
	}
	return c
}
func (s *StatsService) sboxPackets(conn N.PacketConn, user string) N.PacketConn {
	if !sboxManaged.MatchString(user) {
		return conn
	}
	c := &sboxPacket{PacketConn: conn, g: s.guard, sboxMeter: s.meter(user)}
	b, ok := s.guard.add(c, user)
	c.budget = b
	if !ok {
		c.Close()
		c.budget = &sboxBudget{ceiling: 0}
	}
	return c
}

// Expose headroom metadata only. No ReaderReplaceable, WriterReplaceable,
// syscall.Conn or cached-reader fast path may elide this meter.
func (c *sboxPacket) Upstream() any { return c.PacketConn }
func (c *sboxConn) Upstream() any   { return c.Conn }
