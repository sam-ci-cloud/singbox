package v2rayapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	N "github.com/sagernet/sing/common/network"

	"google.golang.org/grpc"
)

type headroomPacket struct{ N.PacketConn }

func (*headroomPacket) FrontHeadroom() int { return 16 }
func (*headroomPacket) RearHeadroom() int  { return 8 }
func TestSboxPacketPreservesProtocolHeadroom(t *testing.T) {
	p := &sboxPacket{PacketConn: &headroomPacket{}}
	if got := N.CalculateFrontHeadroom(p); got != 16 {
		t.Fatalf("front headroom lost: %d", got)
	}
	if got := N.CalculateRearHeadroom(p); got != 8 {
		t.Fatalf("rear headroom lost: %d", got)
	}
}
func TestSboxManagedNamesProtectLegacy(t *testing.T) {
	for _, name := range []string{"legacy", "reality", "vmess", "hy2", "tuic", "anytls", "user-evil", "user-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@unknown"} {
		if sboxManaged.MatchString(name) {
			t.Fatal(name)
		}
	}
	if !sboxManaged.MatchString("user-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@tuic") {
		t.Fatal("managed user rejected")
	}
}
func TestSboxCloseRemovesOnlyItsOwnFlow(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	g := testGuard()
	c := &sboxConn{Conn: a, g: g}
	if _, ok := g.add(c, "user-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa@reality"); !ok {
		t.Fatal("add")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); c.Close() }()
	}
	wg.Wait()
	if len(g.live) != 0 {
		t.Fatal("closed flow retained")
	}
}

// 1.14.4 replaced Server.Close with a lifecycle scope cleanup. The SBOX guard
// must still stop when the scope closes, and a repeated close must be harmless.
func TestSboxScopeCloseStopsGuard(t *testing.T) {
	g := &sboxGuard{blocked: map[string]bool{}, live: map[io.Closer]string{}, stop: make(chan struct{})}
	s := &Server{grpcServer: grpc.NewServer(), statsService: &StatsService{guard: g}}
	scope := adapter.NewScope(context.Background(), log.NewNOPFactory().NewLogger("test"))
	if err := s.Start(adapter.StartStateInitialize, scope); err != nil {
		t.Fatal(err)
	}
	if err := scope.Close(); err != nil {
		t.Fatal("scope close:", err)
	}
	select {
	case <-g.stop:
	default:
		t.Fatal("quota guard still running after scope close")
	}
	g.close()
}

func testGuard() *sboxGuard {
	return &sboxGuard{live: map[io.Closer]string{}, budgets: map[string]*sboxBudget{}, blocked: map[string]bool{}, ready: true, stop: make(chan struct{})}
}

// Native authenticated tracker entry points share the same concrete wrappers
// for all managed tags. Mix directions, TCP and whole UDP datagrams under race.
type memoryStream struct {
	net.Conn
	maxWrite int
}

func (c memoryStream) Write(data []byte) (int, error) {
	if c.maxWrite > 0 && len(data) > c.maxWrite {
		return c.maxWrite, io.ErrUnexpectedEOF
	}
	return len(data), nil
}
func (c memoryStream) Read(data []byte) (int, error) { return len(data), nil }
func (c memoryStream) Close() error                  { return nil }

type memoryPacket struct {
	N.PacketConn
	fail bool
}

func (c memoryPacket) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	b.Extend(73)
	return M.Socksaddr{}, nil
}
func (c memoryPacket) WritePacket(b *buf.Buffer, _ M.Socksaddr) error {
	b.Release()
	if c.fail {
		return errors.New("transport write failed")
	}
	return nil
}
func (c memoryPacket) Close() error { return nil }

func TestSboxConcurrentAccount(t *testing.T) {
	protocols := []string{"reality", "vmess", "hy2", "tuic", "anytls"}
	owner := "user-" + strings.Repeat("a", 32)
	other := "user-" + strings.Repeat("b", 32)
	for _, count := range []int{1, 8, 32} {
		for _, limit := range []int64{0, 1, 8192, 16383, 16384, 16385, 65535, 65536, 65537} {
			t.Run(fmt.Sprintf("sessions%d/budget%d", count, limit), func(t *testing.T) {
				g := testGuard()
				g.budgets[owner] = &sboxBudget{ceiling: limit}
				s := &StatsService{guard: g, users: map[string]bool{}, counters: map[string]*atomic.Int64{}}
				for _, p := range protocols {
					s.users[owner+"@"+p] = true
				}
				var credited atomic.Int64
				var wg sync.WaitGroup
				barrier := make(chan struct{})
				for i := 0; i < count; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						tag := owner + "@" + protocols[i%5]
						c := s.sboxConnection(memoryStream{}, tag)
						p := s.sboxPackets(memoryPacket{}, tag)
						defer c.Close()
						defer p.Close()
						<-barrier
						data := make([]byte, 521)
						for j := 0; j < 200; j++ {
							switch (i + j) % 4 {
							case 0:
								n, _ := c.Read(data)
								credited.Add(int64(n))
							case 1:
								n, _ := c.Write(data)
								credited.Add(int64(n))
							case 2:
								b := buf.New()
								_, e := p.ReadPacket(b)
								if e == nil {
									credited.Add(int64(b.Len()))
								}
								b.Release()
							case 3:
								b := buf.New()
								b.Extend(1389)
								if p.WritePacket(b, M.Socksaddr{}) == nil {
									credited.Add(1389)
								}
							}
						}
					}(i)
				}
				close(barrier)
				wg.Wait()
				if credited.Load() != limit {
					t.Fatalf("credited %d vs limit %d", credited.Load(), limit)
				}
				var total int64
				for _, c := range s.counters {
					total += c.Load()
				}
				if total != limit {
					t.Fatalf("counter sum %d", total)
				}
				q := s.sboxConnection(memoryStream{}, other+"@tuic")
				if n, e := q.Write(make([]byte, 4096)); n != 4096 || e != nil {
					t.Fatalf("other account denied: %d %v", n, e)
				}
				q.Close()
				g.budgets[owner].setCeiling(limit + 25)
				r := s.sboxConnection(memoryStream{}, owner+"@vmess")
				n, _ := r.Write(make([]byte, 32))
				if n != 25 {
					t.Fatalf("top-up %d", n)
				}
				r.Close()
			})
		}
	}
}
func TestSboxTransportRefunds(t *testing.T) {
	g := testGuard()
	user := "user-" + strings.Repeat("c", 32)
	g.budgets[user] = &sboxBudget{ceiling: 20}
	tag := user + "@hy2"
	s := &StatsService{guard: g, users: map[string]bool{tag: true}, counters: map[string]*atomic.Int64{}}
	p := s.sboxPackets(memoryPacket{fail: true}, tag)
	defer p.Close()
	b := buf.New()
	b.Extend(20)
	if p.WritePacket(b, M.Socksaddr{}) == nil {
		t.Fatal("expected failure")
	}
	c := s.sboxConnection(memoryStream{maxWrite: 7}, tag)
	defer c.Close()
	n, e := c.Write(make([]byte, 20))
	if n != 7 || e == nil {
		t.Fatal("expected partial write")
	}
	n, e = c.Read(make([]byte, 100))
	if n != 13 || e != nil {
		t.Fatalf("short read %d %v", n, e)
	}
	if s.counters["user>>>"+tag+">>>traffic>>>downlink"].Load() != 7 || s.counters["user>>>"+tag+">>>traffic>>>uplink"].Load() != 13 {
		t.Fatal("meters disagree")
	}
}
func TestSboxActiveCloseAndReset(t *testing.T) {
	g := testGuard()
	id := "user-" + strings.Repeat("a", 32)
	g.budgets[id] = &sboxBudget{ceiling: 1}
	s := &StatsService{guard: g}
	c := s.sboxConnection(memoryStream{}, id+"@vmess")
	other := s.sboxConnection(memoryStream{}, "user-"+strings.Repeat("b", 32)+"@reality")
	c.Write([]byte{1})
	g.closeExhausted()
	if _, ok := g.live[c]; ok {
		t.Fatal("exhausted flow retained")
	}
	if _, ok := g.live[other]; !ok {
		t.Fatal("other user closed")
	}
	g.budgets[id].setCeiling(4)
	c = s.sboxConnection(memoryStream{}, id+"@tuic")
	if n, _ := c.Write(make([]byte, 9)); n != 3 {
		t.Fatal("reset/topup did not release exact remainder")
	}
	c.Close()
	other.Close()
}
