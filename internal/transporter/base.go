package transporter

import (
	"context"
	"fmt"
	"net"

	"github.com/Ehco1996/ehco/internal/cmgr"
	"github.com/Ehco1996/ehco/internal/conn"
	"github.com/Ehco1996/ehco/internal/constant"
	"github.com/Ehco1996/ehco/internal/lb"
	"github.com/Ehco1996/ehco/internal/metrics"
	"github.com/Ehco1996/ehco/internal/relay/conf"
	"github.com/sagernet/sing-box/common/sniff"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"

	"go.uber.org/zap"
)

var _ RelayServer = &BaseRelayServer{}

type BaseRelayServer struct {
	cmgr cmgr.Cmgr
	cfg  *conf.Config
	l    *zap.SugaredLogger

	remotes lb.RoundRobin
	relayer RelayClient
}

func newBaseRelayServer(cfg *conf.Config, cmgr cmgr.Cmgr) (*BaseRelayServer, error) {
	relayer, err := newRelayClient(cfg)
	if err != nil {
		return nil, err
	}
	return &BaseRelayServer{
		relayer: relayer,
		cfg:     cfg,
		cmgr:    cmgr,
		remotes: cfg.ToRemotesLB(),
		l:       zap.S().Named(cfg.GetLoggerName()),
	}, nil
}

func (b *BaseRelayServer) RelayTCPConn(ctx context.Context, c net.Conn) error {
	remote := b.remotes.Next().Clone()
	metrics.CurConnectionCount.WithLabelValues(remote.Label, metrics.METRIC_CONN_TYPE_TCP).Inc()
	defer metrics.CurConnectionCount.WithLabelValues(remote.Label, metrics.METRIC_CONN_TYPE_TCP).Dec()

	if err := b.checkConnectionLimit(); err != nil {
		return err
	}

	var err error
	c, err = b.sniffAndBlockProtocol(c)
	if err != nil {
		return err
	}
	c = b.applyRateLimit(c)

	rc, err := b.relayer.HandShake(ctx, remote, true)
	if err != nil {
		return fmt.Errorf("handshake error: %w", err)
	}
	defer rc.Close()

	b.l.Infof("RelayTCPConn from %s to %s", c.LocalAddr(), remote.Address)
	return b.handleRelayConn(c, rc, remote, metrics.METRIC_CONN_TYPE_TCP)
}

func (b *BaseRelayServer) RelayUDPConn(ctx context.Context, c net.Conn) error {
	remote := b.remotes.Next().Clone()
	metrics.CurConnectionCount.WithLabelValues(remote.Label, metrics.METRIC_CONN_TYPE_UDP).Inc()
	defer metrics.CurConnectionCount.WithLabelValues(remote.Label, metrics.METRIC_CONN_TYPE_UDP).Dec()

	rc, err := b.relayer.HandShake(ctx, remote, false)
	if err != nil {
		return fmt.Errorf("handshake error: %w", err)
	}
	defer rc.Close()

	b.l.Infof("RelayUDPConn from %s to %s", c.LocalAddr(), remote.Address)
	return b.handleRelayConn(c, rc, remote, metrics.METRIC_CONN_TYPE_UDP)
}

func (b *BaseRelayServer) checkConnectionLimit() error {
	if b.cfg.Options.MaxConnection > 0 && b.cmgr.CountConnection(cmgr.ConnectionTypeActive) >= b.cfg.Options.MaxConnection {
		return fmt.Errorf("relay:%s active connection count exceed limit %d", b.cfg.Label, b.cfg.Options.MaxConnection)
	}
	return nil
}

func (b *BaseRelayServer) sniffAndBlockProtocol(c net.Conn) (net.Conn, error) {
	if len(b.cfg.Options.BlockedProtocols) == 0 {
		return c, nil
	}

	buffer := buf.NewPacket()

	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.Options.SniffTimeout)
	defer cancel()

	sniffMetadata, err := sniff.PeekStream(ctx, c, buffer, b.cfg.Options.SniffTimeout, sniff.TLSClientHello, sniff.HTTPHost)
	if err != nil {
		b.l.Debugf("sniff error: %s", err)
		return c, nil
	}

	if sniffMetadata != nil {
		b.l.Infof("sniffed protocol: %s", sniffMetadata.Protocol)
		for _, p := range b.cfg.Options.BlockedProtocols {
			if sniffMetadata.Protocol == p {
				return c, fmt.Errorf("relay:%s blocked protocol:%s", b.cfg.Label, sniffMetadata.Protocol)
			}
		}
	}

	if !buffer.IsEmpty() {
		return bufio.NewCachedConn(c, buffer), nil
	} else {
		buffer.Release()
	}
	return c, nil
}

func (b *BaseRelayServer) applyRateLimit(c net.Conn) net.Conn {
	if b.cfg.Options.MaxReadRateKbps > 0 {
		return conn.NewRateLimitedConn(c, b.cfg.Options.MaxReadRateKbps)
	}
	return c
}

func (b *BaseRelayServer) handleRelayConn(c, rc net.Conn, remote *lb.Node, connType string) error {
	opts := []conn.RelayConnOption{
		conn.WithLogger(b.l),
		conn.WithRemote(remote),
		conn.WithConnType(connType),
		conn.WithRelayLabel(b.cfg.Label),
		conn.WithRelayOptions(b.cfg.Options),
		conn.WithHandshakeDuration(remote.HandShakeDuration),
	}
	relayConn := conn.NewRelayConn(c, rc, opts...)
	b.cmgr.AddConnection(relayConn)
	defer b.cmgr.RemoveConnection(relayConn)
	return relayConn.Transport()
}

func (b *BaseRelayServer) HealthCheck(ctx context.Context) (int64, error) {
	remote := b.remotes.Next().Clone()
	// us tcp handshake to check health
	_, err := b.relayer.HandShake(ctx, remote, true)
	return int64(remote.HandShakeDuration.Milliseconds()), err
}

func (b *BaseRelayServer) Close() error {
	return fmt.Errorf("not implemented")
}

func (b *BaseRelayServer) ListenAndServe(ctx context.Context) error {
	return fmt.Errorf("not implemented")
}

func NewNetDialer(cfg *conf.Config) *net.Dialer {
	dialer := &net.Dialer{Timeout: constant.DefaultDialTimeOut}
	dialer.SetMultipathTCP(cfg.Options.EnableMultipathTCP)
	return dialer
}

func NewTCPListener(ctx context.Context, cfg *conf.Config) (net.Listener, error) {
	addr, err := net.ResolveTCPAddr("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}
	lcfg := net.ListenConfig{}
	lcfg.SetMultipathTCP(cfg.Options.EnableMultipathTCP)
	return lcfg.Listen(ctx, "tcp", addr.String())
}