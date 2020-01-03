// +build !windows

package dockerproxy

import (
	"strconv"

	model "github.com/DataDog/agent-payload/process"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	"github.com/DataDog/gopsutil/process"
)

// Filter keeps track of every docker-proxy instance and filters network traffic going through them
type Filter struct {
	proxyByPID    map[int32]*proxy
	proxyByTarget map[model.Addr]*proxy
}

type proxy struct {
	pid    int32
	ip     string
	target model.Addr
}

// NewFilter instantiates a new filter loaded with docker-proxy instance information
func NewFilter() *Filter {
	filter := &Filter{
		proxyByPID:    make(map[int32]*proxy),
		proxyByTarget: make(map[model.Addr]*proxy),
	}

	if procs, err := process.AllProcesses(); err == nil {
		filter.LoadProxies(procs)
	} else {
		log.Errorf("error initiating proxy filter: %s", err)
	}

	return filter
}

// LoadProxies by inspecting processes information
func (f *Filter) LoadProxies(procs map[int32]*process.FilledProcess) {
	for _, p := range procs {
		proxy := extractProxyInfo(p)
		if proxy == nil {
			continue
		}

		log.Debugf("detected docker-proxy with pid=%d target.ip=%s target.port=%d",
			proxy.pid,
			proxy.target.Ip,
			proxy.target.Port,
		)

		// Add proxy to cache
		f.proxyByPID[proxy.pid] = proxy
		f.proxyByTarget[proxy.target] = proxy
	}
}

// Filter all connections that have a docker-proxy at one end
func (f *Filter) Filter(payload *model.Connections) {
	original := payload.Conns
	filtered := make([]*model.Connection, 0, len(original))

	for _, c := range original {
		proxy, ok := f.proxyByPID[c.Pid]

		// Infer proxy address (for future use) and move on
		if ok && proxy.ip == "" {
			f.discoverProxyIP(proxy, c)
		}

		// If either end of the connection is a proxy we can drop it
		if f.isProxied(c) {
			continue
		}

		filtered = append(filtered, c)
	}

	payload.Conns = filtered
}

func (f *Filter) isProxied(c *model.Connection) bool {
	if p, ok := f.proxyByTarget[model.Addr{Ip: c.Laddr.Ip, Port: c.Laddr.Port}]; ok {
		return p.ip == c.Raddr.Ip
	}

	if p, ok := f.proxyByTarget[model.Addr{Ip: c.Raddr.Ip, Port: c.Raddr.Port}]; ok {
		return p.ip == c.Laddr.Ip
	}

	return false
}

func (f *Filter) discoverProxyIP(p *proxy, c *model.Connection) {
	// The heuristic here goes as follows:
	// One of the ends of this connections must match p.targetAddr;
	// The proxy IP will be the other end;
	if c.Laddr.Ip == p.target.Ip && c.Laddr.Port == p.target.Port {
		p.ip = c.Raddr.Ip
		return
	}

	if c.Raddr.Ip == p.target.Ip && c.Raddr.Port == p.target.Port {
		p.ip = c.Laddr.Ip
	}
}

func extractProxyInfo(p *process.FilledProcess) *proxy {
	if len(p.Cmdline) == 0 || !strings.EndsWith(p.Cmdline[0], "docker-proxy") {
		return nil
	}

	// Extract proxy target address
	proxy := &proxy{pid: p.Pid}
	for i := 0; i < len(p.Cmdline)-1; i++ {
		switch p.Cmdline(i) {
		case "-container-ip":
			proxy.target.Ip = p.Cmdline[i+1]
		case "-container-port":
			port, err := strconv.Atoi(p.Cmdline[i+1])
			if err != nil {
				return nil
			}
			proxy.target.Port = int32(port)
		}
	}

	if proxy.target.Ip == "" || proxy.target.Ip == 0 {
		return nil
	}

	return proxy
}
