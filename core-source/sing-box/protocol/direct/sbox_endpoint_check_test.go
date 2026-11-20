package direct

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	M "github.com/sagernet/sing/common/metadata"
)

func TestSBOXEndpointCheck(t *testing.T) {
	o := &endpointCheck{slots: make(chan struct{}, 2)}
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Inbound: "hy2-in", InboundType: "hysteria2", User: "fixture"})
	dest := M.Socksaddr{Fqdn: endpointCheckHost, Port: 80}
	dial := func() net.Conn {
		t.Helper()
		conn, err := o.DialContext(ctx, "tcp", dest)
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(time.Second))
		return conn
	}
	for _, traffic := range []struct {
		context context.Context
		network string
		dest    M.Socksaddr
	}{
		{context.Background(), "tcp", dest},
		{adapter.WithContext(context.Background(), &adapter.InboundContext{Inbound: "tuic-in", InboundType: "tuic"}), "tcp", dest},
		{adapter.WithContext(context.Background(), &adapter.InboundContext{Inbound: "anytls-in", InboundType: "anytls", User: "fixture"}), "tcp", dest},
		{ctx, "udp", dest}, {ctx, "tcp", M.Socksaddr{Fqdn: endpointCheckHost, Port: 443}},
		{ctx, "tcp", M.Socksaddr{Fqdn: "elsewhere.invalid", Port: 80}},
	} {
		if c, err := o.DialContext(traffic.context, traffic.network, traffic.dest); err == nil {
			c.Close()
			t.Fatal("accepted unrelated destination, protocol or unauthenticated identity")
		}
	}
	if _, err := o.ListenPacket(ctx, dest); err == nil {
		t.Fatal("accepted UDP")
	}
	good := "HEAD " + endpointCheckPath + " HTTP/1.1\r\nHost: " + endpointCheckHost + "\r\n\r\n"
	c := dial()
	io.WriteString(c, good)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: "HEAD"})
	if err != nil || resp.StatusCode != 204 {
		t.Fatalf("readiness response: %v %v", resp, err)
	}
	resp.Body.Close()
	c.Close()
	for _, req := range []string{strings.Replace(good, "HEAD", "GET", 1), strings.Replace(good, endpointCheckPath, "/other", 1), strings.Replace(good, endpointCheckHost, "elsewhere.invalid", 1), strings.Replace(good, "\r\n\r\n", "\r\nContent-Length: 1\r\n\r\n", 1)} {
		c = dial()
		io.WriteString(c, req)
		one := make([]byte, 1)
		n, _ := c.Read(one)
		c.Close()
		if n != 0 {
			t.Fatal("unrelated request received a response")
		}
	}
}

func TestSBOXEndpointCheckConcurrency(t *testing.T) {
	o := &endpointCheck{slots: make(chan struct{}, 1)}
	ctx := adapter.WithContext(context.Background(), &adapter.InboundContext{Inbound: "tuic-in", InboundType: "tuic", User: "fixture"})
	dest := M.Socksaddr{Fqdn: endpointCheckHost, Port: 80}
	c, err := o.DialContext(ctx, "tcp", dest)
	if err != nil {
		t.Fatal(err)
	}
	if extra, err := o.DialContext(ctx, "tcp", dest); err == nil {
		extra.Close()
		t.Fatal("unbounded parallel requests")
	}
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if n, err := c.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("idle request was not closed by responder: %d %v", n, err)
	}
	c.Close()
}
