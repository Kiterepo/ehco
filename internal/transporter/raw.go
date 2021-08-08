package transporter

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Ehco1996/ehco/internal/constant"
	"github.com/Ehco1996/ehco/internal/lb"
	"github.com/Ehco1996/ehco/internal/logger"
	"github.com/Ehco1996/ehco/internal/web"
	"github.com/gobwas/ws"
)

type Raw struct {
	udpmu          sync.Mutex
	TCPRemotes     lb.RoundRobin
	UDPRemotes     lb.RoundRobin
	UDPBufferChMap map[string]*BufferCh
}

func (raw *Raw) GetOrCreateBufferCh(uaddr *net.UDPAddr) *BufferCh {
	raw.udpmu.Lock()
	defer raw.udpmu.Unlock()

	bc, found := raw.UDPBufferChMap[uaddr.String()]
	if !found {
		bc := newudpBufferCh(uaddr)
		raw.UDPBufferChMap[uaddr.String()] = bc
		return bc
	}
	return bc
}

func (raw *Raw) HandleUDPConn(uaddr *net.UDPAddr, local *net.UDPConn) {
	remote := raw.UDPRemotes.Next()
	web.CurUDPNum.WithLabelValues(remote).Inc()
	defer web.CurUDPNum.WithLabelValues(remote).Dec()

	bc := raw.GetOrCreateBufferCh(uaddr)
	remoteUdp, _ := net.ResolveUDPAddr("udp", remote)
	rc, err := net.DialUDP("udp", nil, remoteUdp)
	if err != nil {
		logger.Info(err)
		return
	}
	defer func() {
		rc.Close()
		delete(raw.UDPBufferChMap, uaddr.String())
	}()

	logger.Infof("[raw] HandleUDPConn from %s to %s", local.LocalAddr().String(), remoteUdp.String())

	buf := BufferPool.Get()
	defer BufferPool.Put(buf)

	var wg sync.WaitGroup
	wg.Add(1)
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		defer wg.Done()
		defer cancel()
		wt := 0
		for {
			_ = rc.SetDeadline(time.Now().Add(constant.DefaultDeadline))
			i, err := rc.Read(buf)
			if err != nil {
				logger.Info(err)
				break
			}
			if _, err := local.WriteToUDP(buf[0:i], uaddr); err != nil {
				logger.Info(err)
				break
			}
			wt += i
		}
		web.NetWorkTransmitBytes.WithLabelValues(remote).Add(float64(wt * 2))
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		wt := 0
		select {
		case <-ctx.Done():
			return
		case b := <-bc.Ch:
			wt += len(b)
			if _, err := rc.Write(b); err != nil {
				logger.Info(err)
				return
			}
			_ = rc.SetDeadline(time.Now().Add(constant.DefaultDeadline))
		}
		web.NetWorkTransmitBytes.WithLabelValues(remote).Add(float64(wt * 2))
	}()

	wg.Wait()
}

func (raw *Raw) HandleTCPConn(c *net.TCPConn) error {
	defer c.Close()
	remote := raw.TCPRemotes.Next()
	web.CurTCPNum.WithLabelValues(remote).Inc()
	defer web.CurTCPNum.WithLabelValues(remote).Dec()

	rc, err := net.Dial("tcp", remote)
	if err != nil {
		return err
	}
	logger.Infof("[raw] HandleTCPConn from %s to %s", c.LocalAddr().String(), remote)
	defer rc.Close()

	return transport(c, rc, remote)
}

func (raw *Raw) HandleWsRequset(w http.ResponseWriter, req *http.Request) {
	wsc, _, _, err := ws.UpgradeHTTP(req, w)
	if err != nil {
		return
	}
	defer wsc.Close()
	remote := raw.TCPRemotes.Next()
	web.CurTCPNum.WithLabelValues(remote).Inc()
	defer web.CurTCPNum.WithLabelValues(remote).Dec()

	rc, err := net.Dial("tcp", remote)
	if err != nil {
		logger.Infof("dial error: %s", err)
		return
	}
	defer rc.Close()

	logger.Infof("[tun] HandleWsRequset from:%s to:%s", wsc.RemoteAddr(), rc.RemoteAddr())
	if err := transportWithTimeOut(rc, wsc, remote); err != nil {
		logger.Infof("[tun] HandleWsRequset err: %s", err.Error())
	}
}

func (raw *Raw) HandleWssRequset(w http.ResponseWriter, req *http.Request) {
	wsc, _, _, err := ws.UpgradeHTTP(req, w)
	if err != nil {
		return
	}
	defer wsc.Close()
	remote := raw.TCPRemotes.Next()
	web.CurTCPNum.WithLabelValues(remote).Inc()
	defer web.CurTCPNum.WithLabelValues(remote).Dec()

	rc, err := net.Dial("tcp", remote)
	if err != nil {
		logger.Infof("dial error: %s", err)
		return
	}
	defer rc.Close()

	logger.Infof("[tun] HandleWssRequset from:%s to:%s", wsc.RemoteAddr(), rc.RemoteAddr())
	if err := transport(rc, wsc, remote); err != nil {
		logger.Infof("[tun] HandleWssRequset err: %s", err.Error())
	}
}

func (raw *Raw) HandleMWssRequset(c net.Conn) {
	defer c.Close()
	remote := raw.TCPRemotes.Next()
	web.CurTCPNum.WithLabelValues(remote).Inc()
	defer web.CurTCPNum.WithLabelValues(remote).Dec()

	rc, err := net.Dial("tcp", remote)
	if err != nil {
		logger.Infof("dial error: %s", err)
		return
	}
	defer rc.Close()

	logger.Infof("[tun] HandleMWssRequset from:%s to:%s", c.RemoteAddr(), rc.RemoteAddr())
	if err := transport(rc, c, remote); err != nil {
		logger.Infof("[tun] HandleMWssRequset err: %s", err.Error())
	}
}
