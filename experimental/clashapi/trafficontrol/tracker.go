package trafficontrol

import (
	"net"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/experimental/trackerconn"
	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"

	"github.com/gofrs/uuid"
	"go.uber.org/atomic"
)

type Metadata struct {
	NetWork     string     `json:"network"`
	Type        string     `json:"type"`
	SrcIP       netip.Addr `json:"sourceIP"`
	DstIP       netip.Addr `json:"destinationIP"`
	SrcPort     string     `json:"sourcePort"`
	DstPort     string     `json:"destinationPort"`
	Host        string     `json:"host"`
	DNSMode     string     `json:"dnsMode"`
	ProcessPath string     `json:"processPath"`
}

type tracker interface {
	ID() string
	Close() error
	Leave()
}

type trackerInfo struct {
	UUID          uuid.UUID     `json:"id"`
	Metadata      Metadata      `json:"metadata"`
	UploadTotal   *atomic.Int64 `json:"upload"`
	DownloadTotal *atomic.Int64 `json:"download"`
	Start         time.Time     `json:"start"`
	Chain         []string      `json:"chains"`
	Rule          string        `json:"rule"`
	RulePayload   string        `json:"rulePayload"`
}

type tcpTracker struct {
	N.ExtendedConn `json:"-"`
	*trackerInfo
	manager *Manager
}

func (tt *tcpTracker) ID() string {
	return tt.UUID.String()
}

func (tt *tcpTracker) Close() error {
	tt.manager.Leave(tt)
	return tt.ExtendedConn.Close()
}

func (tt *tcpTracker) Leave() {
	tt.manager.Leave(tt)
}

func (tt *tcpTracker) Upstream() any {
	return tt.ExtendedConn
}

func (tt *tcpTracker) ReaderReplaceable() bool {
	return true
}

func (tt *tcpTracker) WriterReplaceable() bool {
	return true
}

func NewTCPTracker(conn net.Conn, manager *Manager, metadata Metadata, router adapter.Router, rule adapter.Rule, directIO bool) *tcpTracker {
	uuid, _ := uuid.NewV4()

	var chain []string
	var next string
	if rule == nil {
		next = router.DefaultOutbound(N.NetworkTCP).Tag()
	} else {
		next = rule.Outbound()
	}
	for {
		chain = append(chain, next)
		detour, loaded := router.Outbound(next)
		if !loaded {
			break
		}
		group, isGroup := detour.(adapter.OutboundGroup)
		if !isGroup {
			break
		}
		next = group.Now()
	}

	upload := atomic.NewInt64(0)
	download := atomic.NewInt64(0)

	t := &tcpTracker{
		ExtendedConn: trackerconn.NewHook(conn, func(n int64) {
			upload.Add(n)
			manager.PushUploaded(n)
		}, func(n int64) {
			download.Add(n)
			manager.PushDownloaded(n)
		}, directIO),
		manager: manager,
		trackerInfo: &trackerInfo{
			UUID:          uuid,
			Start:         time.Now(),
			Metadata:      metadata,
			Chain:         common.Reverse(chain),
			Rule:          "",
			UploadTotal:   upload,
			DownloadTotal: download,
		},
	}

	if rule != nil {
		t.trackerInfo.Rule = rule.String() + " => " + rule.Outbound()
	} else {
		t.trackerInfo.Rule = "final"
	}

	manager.Join(t)
	return t
}

type udpTracker struct {
	N.PacketConn `json:"-"`
	*trackerInfo
	manager *Manager
}

func (ut *udpTracker) ID() string {
	return ut.UUID.String()
}

func (ut *udpTracker) Close() error {
	ut.manager.Leave(ut)
	return ut.PacketConn.Close()
}

func (ut *udpTracker) Leave() {
	ut.manager.Leave(ut)
}

func (ut *udpTracker) Upstream() any {
	return ut.PacketConn
}

func (ut *udpTracker) ReaderReplaceable() bool {
	return true
}

func (ut *udpTracker) WriterReplaceable() bool {
	return true
}

func NewUDPTracker(conn N.PacketConn, manager *Manager, metadata Metadata, router adapter.Router, rule adapter.Rule) *udpTracker {
	uuid, _ := uuid.NewV4()

	var chain []string
	var next string
	if rule == nil {
		next = router.DefaultOutbound(N.NetworkUDP).Tag()
	} else {
		next = rule.Outbound()
	}
	for {
		chain = append(chain, next)
		detour, loaded := router.Outbound(next)
		if !loaded {
			break
		}
		group, isGroup := detour.(adapter.OutboundGroup)
		if !isGroup {
			break
		}
		next = group.Now()
	}

	upload := atomic.NewInt64(0)
	download := atomic.NewInt64(0)

	ut := &udpTracker{
		PacketConn: trackerconn.NewHookPacket(conn, func(n int64) {
			upload.Add(n)
			manager.PushUploaded(n)
		}, func(n int64) {
			download.Add(n)
			manager.PushDownloaded(n)
		}),
		manager: manager,
		trackerInfo: &trackerInfo{
			UUID:          uuid,
			Start:         time.Now(),
			Metadata:      metadata,
			Chain:         common.Reverse(chain),
			Rule:          "",
			UploadTotal:   upload,
			DownloadTotal: download,
		},
	}

	if rule != nil {
		ut.trackerInfo.Rule = rule.String() + " => " + rule.Outbound()
	} else {
		ut.trackerInfo.Rule = "final"
	}

	manager.Join(ut)
	return ut
}
