//go:build with_wireguard

package outbound

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/dialer"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/transport/wireguard"
	"github.com/sagernet/sing-tun"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/debug"
	E "github.com/sagernet/sing/common/exceptions"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	"golang.zx2c4.com/wireguard/device"
)

var _ adapter.Outbound = (*WireGuard)(nil)

type WireGuard struct {
	myOutboundAdapter
	bind      *wireguard.ClientBind
	device    *device.Device
	tunDevice wireguard.Device
}

func NewWireGuard(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.WireGuardOutboundOptions) (*WireGuard, error) {
	outbound := &WireGuard{
		myOutboundAdapter: myOutboundAdapter{
			protocol: C.TypeWireGuard,
			network:  options.Network.Build(),
			router:   router,
			logger:   logger,
			tag:      tag,
		},
	}
	var reserved [3]uint8
	if len(options.Reserved) > 0 {
		if len(options.Reserved) != 3 {
			return nil, E.New("invalid reserved value, required 3 bytes, got ", len(options.Reserved))
		}
		copy(reserved[:], options.Reserved)
	}
	peerAddr := options.ServerOptions.Build()
	outbound.bind = wireguard.NewClientBind(ctx, dialer.New(router, options.DialerOptions), peerAddr, reserved)
	localPrefixes := common.Map(options.LocalAddress, option.ListenPrefix.Build)
	if len(localPrefixes) == 0 {
		return nil, E.New("missing local address")
	}
	var privateKey, peerPublicKey, preSharedKey string
	{
		bytes, err := base64.StdEncoding.DecodeString(options.PrivateKey)
		if err != nil {
			return nil, E.Cause(err, "decode private key")
		}
		privateKey = hex.EncodeToString(bytes)
	}
	{
		bytes, err := base64.StdEncoding.DecodeString(options.PeerPublicKey)
		if err != nil {
			return nil, E.Cause(err, "decode peer public key")
		}
		peerPublicKey = hex.EncodeToString(bytes)
	}
	if options.PreSharedKey != "" {
		bytes, err := base64.StdEncoding.DecodeString(options.PreSharedKey)
		if err != nil {
			return nil, E.Cause(err, "decode pre shared key")
		}
		preSharedKey = hex.EncodeToString(bytes)
	}
	ipcConf := "private_key=" + privateKey
	ipcConf += "\npublic_key=" + peerPublicKey
	ipcConf += "\nendpoint=" + peerAddr.String()
	if preSharedKey != "" {
		ipcConf += "\npreshared_key=" + preSharedKey
	}
	var has4, has6 bool
	for _, address := range localPrefixes {
		if address.Addr().Is4() {
			has4 = true
		} else {
			has6 = true
		}
	}
	if has4 {
		ipcConf += "\nallowed_ip=0.0.0.0/0"
	}
	if has6 {
		ipcConf += "\nallowed_ip=::/0"
	}
	mtu := options.MTU
	if mtu == 0 {
		mtu = 1408
	}
	var wireTunDevice wireguard.Device
	var err error
	if !options.SystemInterface && tun.WithGVisor {
		wireTunDevice, err = wireguard.NewStackDevice(localPrefixes, mtu)
	} else {
		wireTunDevice, err = wireguard.NewSystemDevice(router, options.InterfaceName, localPrefixes, mtu)
	}
	if err != nil {
		return nil, E.Cause(err, "create WireGuard device")
	}
	wgDevice := device.NewDevice(wireTunDevice, outbound.bind, &device.Logger{
		Verbosef: func(format string, args ...interface{}) {
			logger.Debug(fmt.Sprintf(strings.ToLower(format), args...))
		},
		Errorf: func(format string, args ...interface{}) {
			logger.Error(fmt.Sprintf(strings.ToLower(format), args...))
		},
	})
	if debug.Enabled {
		logger.Trace("created wireguard ipc conf: \n", ipcConf)
	}
	err = wgDevice.IpcSet(ipcConf)
	if err != nil {
		return nil, E.Cause(err, "setup wireguard")
	}
	outbound.device = wgDevice
	outbound.tunDevice = wireTunDevice
	return outbound, nil
}

func (w *WireGuard) DialContext(ctx context.Context, network string, destination M.Socksaddr) (net.Conn, error) {
	switch network {
	case N.NetworkTCP:
		w.logger.InfoContext(ctx, "outbound connection to ", destination)
	case N.NetworkUDP:
		w.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	}
	if destination.IsFqdn() {
		addrs, err := w.router.LookupDefault(ctx, destination.Fqdn)
		if err != nil {
			return nil, err
		}
		return N.DialSerial(ctx, w.tunDevice, network, destination, addrs)
	}
	return w.tunDevice.DialContext(ctx, network, destination)
}

func (w *WireGuard) ListenPacket(ctx context.Context, destination M.Socksaddr) (net.PacketConn, error) {
	w.logger.InfoContext(ctx, "outbound packet connection to ", destination)
	return w.tunDevice.ListenPacket(ctx, destination)
}

func (w *WireGuard) NewConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	return NewConnection(ctx, w, conn, metadata)
}

func (w *WireGuard) NewPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	return NewPacketConnection(ctx, w, conn, metadata)
}

func (w *WireGuard) Start() error {
	return w.tunDevice.Start()
}

func (w *WireGuard) Close() error {
	if w.device != nil {
		w.device.Close()
	}
	return common.Close(w.tunDevice)
}
