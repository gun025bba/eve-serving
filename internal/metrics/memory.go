package metrics

import (
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

// MemSampler reads system memory and the RSS of the mlx_lm.server process.
type MemSampler struct {
	mu     sync.Mutex
	pid    int32
	Needle string // substring matched in the upstream process cmdline
}

type MemInfo struct {
	TotalGB  float64  `json:"total_gb"`
	AvailGB  float64  `json:"available_gb"`
	Percent  float64  `json:"percent"`
	MLXRSSGB *float64 `json:"mlx_server_rss_gb"`
}

func (s *MemSampler) mlxRSS() (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pid != 0 {
		if p, err := process.NewProcess(s.pid); err == nil {
			if mi, err := p.MemoryInfo(); err == nil {
				return mi.RSS, true
			}
		}
		s.pid = 0
	}
	ps, err := process.Processes()
	if err != nil {
		return 0, false
	}
	for _, p := range ps {
		cl, err := p.Cmdline()
		if err != nil || !strings.Contains(cl, s.Needle) {
			continue
		}
		if mi, err := p.MemoryInfo(); err == nil {
			s.pid = p.Pid
			return mi.RSS, true
		}
	}
	return 0, false
}

// Sample reads memory and updates the gauges in m (m may be nil).
func (s *MemSampler) Sample(m *Metrics) MemInfo {
	out := MemInfo{}
	if vm, err := mem.VirtualMemory(); err == nil {
		out.TotalGB = Round(float64(vm.Total)/1e9, 1)
		out.AvailGB = Round(float64(vm.Available)/1e9, 1)
		out.Percent = Round(vm.UsedPercent, 1)
		if m != nil {
			m.MemSys.WithLabelValues("total").Set(float64(vm.Total))
			m.MemSys.WithLabelValues("available").Set(float64(vm.Available))
			m.MemSys.WithLabelValues("used").Set(float64(vm.Total - vm.Available))
		}
	}
	if rss, ok := s.mlxRSS(); ok && rss > 0 {
		g := Round(float64(rss)/1e9, 2)
		out.MLXRSSGB = &g
		if m != nil {
			m.MemMLX.Set(float64(rss))
		}
	}
	return out
}
