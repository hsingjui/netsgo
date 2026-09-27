package server

import (
	"fmt"
	"time"

	"github.com/gorilla/websocket"

	"netsgo/pkg/protocol"
)

const defaultControlWriteTimeout = 2 * time.Second

func (c *ClientConn) GetInfo() protocol.ClientInfo {
	c.infoMu.RLock()
	defer c.infoMu.RUnlock()
	return c.Info
}

func (c *ClientConn) SetInfo(info protocol.ClientInfo) {
	c.infoMu.Lock()
	c.Info = info
	c.infoMu.Unlock()
}

func (c *ClientConn) GetBandwidthSettings() protocol.BandwidthSettings {
	c.bandwidthMu.RLock()
	defer c.bandwidthMu.RUnlock()
	return c.bandwidth
}

func (c *ClientConn) SetBandwidthSettings(settings protocol.BandwidthSettings) error {
	if err := validateBandwidthSettings(settings); err != nil {
		return err
	}

	c.bandwidthMu.Lock()
	defer c.bandwidthMu.Unlock()

	c.bandwidth = settings
	if c.bandwidthRT == nil {
		c.bandwidthRT = newDirectionalBandwidthRuntime(settings, realBandwidthClock{})
		return nil
	}

	c.bandwidthRT.Update(settings)
	return nil
}

func (c *ClientConn) BandwidthRuntime() *directionalBandwidthRuntime {
	c.bandwidthMu.RLock()
	defer c.bandwidthMu.RUnlock()
	return c.bandwidthRT
}

func (c *ClientConn) writeJSON(v any) error {
	return c.writeJSONBefore(v, time.Time{})
}

func (c *ClientConn) writeJSONBefore(v any, deadline time.Time) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("client %s control channel unavailable", c.ID)
	}
	if deadline.IsZero() {
		deadline = time.Now().Add(defaultControlWriteTimeout)
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		c.detachAndCloseControlConn(conn)
		return fmt.Errorf("set control write deadline: %w", err)
	}
	defer func() { _ = conn.SetWriteDeadline(time.Time{}) }()
	if err := conn.WriteJSON(v); err != nil {
		c.detachAndCloseControlConn(conn)
		return err
	}
	return nil
}

func (c *ClientConn) detachAndCloseControlConn(conn *websocket.Conn) {
	c.mu.Lock()
	if c.conn == conn {
		c.conn = nil
	}
	c.mu.Unlock()
	_ = conn.Close()
}

func (c *ClientConn) detachControlConn() *websocket.Conn {
	c.mu.Lock()
	defer c.mu.Unlock()
	conn := c.conn
	c.conn = nil
	return conn
}

func (a *ClientConn) SetStats(s *protocol.SystemStats) {
	a.statsMu.Lock()
	a.stats = s
	a.statsMu.Unlock()
}

func (a *ClientConn) GetStats() *protocol.SystemStats {
	a.statsMu.RLock()
	defer a.statsMu.RUnlock()
	return a.stats
}

func (a *ClientConn) enrichStats(stats *protocol.SystemStats) {
	a.statsMu.RLock()
	prev := a.prevStats
	prevAt := a.prevStatsAt
	a.statsMu.RUnlock()

	if prev != nil {
		elapsed := time.Since(prevAt).Seconds()
		if elapsed > 0.5 {
			if stats.NetSent >= prev.NetSent {
				stats.NetSentSpeed = float64(stats.NetSent-prev.NetSent) / elapsed
			}
			if stats.NetRecv >= prev.NetRecv {
				stats.NetRecvSpeed = float64(stats.NetRecv-prev.NetRecv) / elapsed
			}
		}
	}
}

func (a *ClientConn) RangeProxies(fn func(name string, tunnel *ProxyTunnel) bool) {
	a.proxyMu.RLock()
	defer a.proxyMu.RUnlock()
	for name, tunnel := range a.proxies {
		if !fn(name, tunnel) {
			return
		}
	}
}
