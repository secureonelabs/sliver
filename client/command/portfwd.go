package command

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"time"

	"github.com/bishopfox/sliver/client/core"
	"github.com/bishopfox/sliver/protobuf/clientpb"
	"github.com/bishopfox/sliver/protobuf/commonpb"
	"github.com/bishopfox/sliver/protobuf/rpcpb"
	"github.com/bishopfox/sliver/protobuf/sliverpb"
	"github.com/desertbit/grumble"
)

func portfwd(ctx *grumble.Context, rpc rpcpb.SliverRPCClient) {
	fmt.Printf("Port Forwards\n")
	for _, portfwd := range core.Portfwds.List() {
		fmt.Printf("%s:%d\n", portfwd.Protocol, portfwd.Port)
	}
}

func portfwdAdd(ctx *grumble.Context, rpc rpcpb.SliverRPCClient) {
	session := ActiveSession.Get()
	if session == nil {
		return
	}
	if session.GetActiveC2() == "dns" {
		fmt.Printf(Warn + "Current C2 is DNS, this is going to be a very slow tunnel!\n")
	}

}

func portfwdRm(ctx *grumble.Context, rpc rpcpb.SliverRPCClient) {

}

//
//
type ChannelProxy struct {
	rpc     rpcpb.SliverRPCClient
	session *clientpb.Session

	Addr            string
	KeepAlivePeriod time.Duration
	DialTimeout     time.Duration
}

func (p *ChannelProxy) HandleConn(conn net.Conn) {
	log.Printf("[tcpproxy] Handling new connection")
	ctx := context.Background()
	var cancel context.CancelFunc
	if p.DialTimeout >= 0 {
		ctx, cancel = context.WithTimeout(ctx, p.dialTimeout())
	}
	tunnel, err := p.dialImplant(ctx)
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return
	}

	// Cleanup
	defer func() {
		go conn.Close()
		core.Tunnels.Close(tunnel.ID)
	}()

	errs := make(chan error, 1)
	go toImplantLoop(conn, tunnel, errs)
	go fromImplantLoop(conn, tunnel, errs)

	// Block until error, then cleanup
	err = <-errs
	if err != nil {
		log.Printf("[tcpproxy] Closing tunnel %d with error %s", tunnel.ID, err)
	}
}

func (p *ChannelProxy) Port() uint32 {
	defaultPort := uint32(80)
	_, rawPort, err := net.SplitHostPort(p.Addr)
	if err != nil {
		log.Printf("Failed to parse addr %s", p.Addr)
		return defaultPort
	}
	portNumber, err := strconv.Atoi(rawPort)
	if err != nil {
		log.Printf("Failed to parse number from %s", rawPort)
		return defaultPort
	}
	port := uint32(portNumber)
	if port < 1 || 65535 < port {
		log.Printf("Invalid port number %d", port)
		return defaultPort
	}
	return port
}

func (p *ChannelProxy) dialImplant(ctx context.Context) (*core.Tunnel, error) {

	log.Printf("[tcpproxy] Dialing implant to create tunnel ...")

	// Create an RPC tunnel, then start it before binding the shell to the newly created tunnel
	rpcTunnel, err := p.rpc.CreateTunnel(ctx, &sliverpb.Tunnel{
		SessionID: p.session.ID,
	})
	if err != nil {
		log.Printf("[tcpproxy] Failed to dial implant %s", err)
		return nil, err
	}
	log.Printf("[tcpproxy] Created new tunnel with id %d (session %d)", rpcTunnel.TunnelID, p.session.ID)
	tunnel := core.Tunnels.Start(rpcTunnel.TunnelID, rpcTunnel.SessionID)

	log.Printf("[tcpproxy] Binding tunnel to portfwd %d", p.Port())
	portfwdResp, err := p.rpc.Portfwd(ctx, &sliverpb.PortfwdReq{
		Request: &commonpb.Request{
			SessionID: p.session.ID,
		},
		Port:     p.Port(),
		Protocol: sliverpb.PortfwdProtocol_TCP,
		TunnelID: tunnel.ID,
	})
	if err != nil {
		return nil, err
	}
	log.Printf("Portfwd response: %v", portfwdResp)

	return tunnel, nil
}

func (p *ChannelProxy) keepAlivePeriod() time.Duration {
	if p.KeepAlivePeriod != 0 {
		return p.KeepAlivePeriod
	}
	return time.Minute
}

func (p *ChannelProxy) dialTimeout() time.Duration {
	if p.DialTimeout > 0 {
		return p.DialTimeout
	}
	return 30 * time.Second
}

func toImplantLoop(conn net.Conn, tunnel *core.Tunnel, errs chan<- error) {
	n, err := io.Copy(tunnel, conn)
	log.Printf("[tcpproxy] Closing to-implant after %d byte(s)", n)
	errs <- err
}

func fromImplantLoop(conn net.Conn, tunnel *core.Tunnel, errs chan<- error) {
	n, err := io.Copy(conn, tunnel)
	log.Printf("[tcpproxy] Closing from-implant after %d byte(s)", n)
	errs <- err
}