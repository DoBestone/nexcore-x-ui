package service

import (
	"github.com/shirou/gopsutil/v4/net"
)

type SystemService struct{}

type ListeningPort struct {
	Family string `json:"family"` // tcp/udp/tcp6/udp6
	Addr   string `json:"addr"`
	Port   uint32 `json:"port"`
	Pid    int32  `json:"pid"`
	Status string `json:"status"`
}

// ListeningPorts returns every LISTEN-state TCP socket plus all UDP sockets.
// The returned set is what the panel sees from gopsutil — it includes ports
// owned by other processes too, which is exactly what an operator needs to
// avoid picking a colliding port for a new inbound.
func (s *SystemService) ListeningPorts() ([]ListeningPort, error) {
	out := make([]ListeningPort, 0)
	for _, proto := range []string{"tcp", "tcp6", "udp", "udp6"} {
		conns, err := net.Connections(proto)
		if err != nil {
			continue
		}
		for _, conn := range conns {
			isTCP := proto == "tcp" || proto == "tcp6"
			if isTCP && conn.Status != "LISTEN" {
				continue
			}
			out = append(out, ListeningPort{
				Family: proto,
				Addr:   conn.Laddr.IP,
				Port:   conn.Laddr.Port,
				Pid:    conn.Pid,
				Status: conn.Status,
			})
		}
	}
	return out, nil
}
