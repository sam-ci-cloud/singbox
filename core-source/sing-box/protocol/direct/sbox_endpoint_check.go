package direct

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/outbound"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// The server-side transport check never opens a network socket. Its reserved
// destination is usable only after a named SBOX QUIC inbound authenticates.
const (
	endpointCheckType = "sbox-endpoint-check"
	endpointCheckHost = "endpoint-check.sbox.invalid"
	endpointCheckPath = "/readiness"
)

type endpointCheck struct {
	outbound.Adapter
	slots chan struct{}
}

func registerEndpointCheck(registry *outbound.Registry) {
	outbound.Register[option.StubOptions](registry, endpointCheckType, newEndpointCheck)
}

func newEndpointCheck(_ context.Context, _ adapter.Router, _ log.ContextLogger, tag string, _ option.StubOptions) (adapter.Outbound, error) {
	return &endpointCheck{
		Adapter: outbound.NewAdapter(endpointCheckType, tag, []string{N.NetworkTCP}, nil),
		slots:   make(chan struct{}, 64),
	}, nil
}

func (o *endpointCheck) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	m := adapter.ContextFrom(ctx)
	if network != N.NetworkTCP || destination.Fqdn != endpointCheckHost || destination.Port != 80 ||
		m == nil || m.User == "" || !(m.Inbound == "hy2-in" && m.InboundType == "hysteria2" || m.Inbound == "tuic-in" && m.InboundType == "tuic") {
		return nil, os.ErrPermission
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case o.slots <- struct{}{}:
	default:
		return nil, os.ErrDeadlineExceeded
	}
	client, server := net.Pipe()
	go func() {
		defer func() { <-o.slots }()
		defer server.Close()
		server.SetDeadline(time.Now().Add(2 * time.Second))
		// A single, bounded request. Invalid requests get EOF, so HTTP clients
		// cannot mistake an error page or authentication challenge for success.
		req, err := http.ReadRequest(bufio.NewReader(io.LimitReader(server, 2048)))
		if err != nil {
			return
		}
		defer req.Body.Close()
		if req.Method != http.MethodHead || req.Host != endpointCheckHost || req.RequestURI != endpointCheckPath ||
			req.ContentLength != 0 || len(req.TransferEncoding) != 0 {
			return
		}
		io.WriteString(server, "HTTP/1.1 204 No Content\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n")
	}()
	return client, nil
}

func (*endpointCheck) ListenPacket(context.Context, M.Socksaddr) (net.PacketConn, error) {
	return nil, os.ErrPermission
}
