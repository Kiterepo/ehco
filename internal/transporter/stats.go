package transporter

import (
	"fmt"

	"github.com/Ehco1996/ehco/pkg/bytes"
)

type Stats struct {
	up   int64
	down int64
}

func (s *Stats) ReSet() {
	s.up = 0
	s.down = 0
}

func (s *Stats) String() string {
	return fmt.Sprintf("up: %s, down: %s", bytes.PrettyByteSize(float64(s.up)), bytes.PrettyByteSize(float64(s.down)))
}

type ConnStats interface {
	RecordTraffic(down, up int64)

	ReSetTraffic()

	GetStats() *Stats
}

func NewConnStats() ConnStats {
	return &connStatsImpl{s: &Stats{up: 0, down: 0}}
}

type connStatsImpl struct {
	s *Stats
}

func (c *connStatsImpl) RecordTraffic(down, up int64) {
	c.s.down += down
	c.s.up += up
}

func (c *connStatsImpl) ReSetTraffic() {
	c.s.ReSet()
}

func (c *connStatsImpl) GetStats() *Stats {
	return c.s
}