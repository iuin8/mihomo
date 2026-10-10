//go:build with_gvisor

// FORK(easytier-tun): gvisor 版包装器。
//
// sing-tun 的 gvisor 栈（stack_gvisor.go:50）会对传入的设备做类型断言：
//
//	gTun, isGTun := options.Tun.(GVisorTun)   // Tun + WritePacket + NewEndpoint
//
// 只内嵌 tun.Tun 接口的包装器断言必然失败，于是栈拒绝启动并报
// "gVisor stack is unsupported on current platform"，TUN 被回写成关闭 ——
// 表现为注入口已发布、包却永远没人读。所以这里把两个方法**原样透传**给真设备。
package easytier

import (
	"fmt"
	"sync"

	"github.com/metacubex/gvisor/pkg/buffer"
	"github.com/metacubex/gvisor/pkg/tcpip/header"
	"github.com/metacubex/gvisor/pkg/tcpip/stack"
	tun "github.com/metacubex/sing-tun"
)

// injectableTun 既是 tun.Tun（内嵌 base 透传 Read/Write/Close），又满足 GVisorTun。
type injectableTun struct {
	*injectableTunBase
	device tun.Tun
}

// WrapTunForIngress 包装宿主 TUN。返回值交给 sing-tun 的栈使用，第二个返回值是注入口。
func WrapTunForIngress(device tun.Tun) (tun.Tun, IngressSink) {
	wrapped := &injectableTun{injectableTunBase: newInjectableTunBase(device), device: device}
	return wrapped, wrapped
}

func (t *injectableTun) WritePacket(packet *stack.PacketBuffer) (int, error) {
	sender, ok := t.device.(interface {
		WritePacket(*stack.PacketBuffer) (int, error)
	})
	if !ok {
		return 0, fmt.Errorf("easytier: wrapped TUN %T does not support WritePacket", t.device)
	}
	return sender.WritePacket(packet)
}

func (t *injectableTun) NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error) {
	creator, ok := t.device.(interface {
		NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error)
	})
	if !ok {
		return nil, stack.NICOptions{}, fmt.Errorf("easytier: wrapped TUN %T does not support NewEndpoint", t.device)
	}
	real, opts, err := creator.NewEndpoint()
	if err != nil {
		return nil, opts, err
	}
	// gvisor 栈读的是这个 endpoint（真设备 fd），不会走 injectableTunBase.Read ——
	// 所以入向要由我们的 endpoint 额外投喂，同时停掉 pump（同一个 fd 不能有两个读者）。
	t.injectableTunBase.stopPump()
	return &injectableEndpoint{LinkEndpoint: real, base: t.injectableTunBase}, opts, nil
}

// injectableEndpoint 包住真 endpoint：入向仍由真 endpoint 独占读（同一个 fd 不能有两个读者），
// 我们只是**额外**把注入队列里的包交给 gvisor 栈。
//
// 为什么必须这样：栈拿到的是真 endpoint（fdbased，读 tun fd），它**从不读** injectableTunBase.Read —
// 于是早先"包进了注入队列却没人读"（实测 injected=9 consumed=0）。
type injectableEndpoint struct {
	stack.LinkEndpoint // 其余方法全部透传（MTU / WritePackets / …）
	base               *injectableTunBase
	mu                 sync.RWMutex
	dispatcher         stack.NetworkDispatcher
	once               sync.Once
}

var _ stack.LinkEndpoint = (*injectableEndpoint)(nil)

// Attach 记住 dispatcher：栈挂上来之后我们才有一条把注入包交给它的路。
func (e *injectableEndpoint) Attach(dispatcher stack.NetworkDispatcher) {
	e.mu.Lock()
	e.dispatcher = dispatcher
	e.mu.Unlock()
	e.LinkEndpoint.Attach(dispatcher) // 真设备照常工作（内核来的包仍由它投递）
	e.once.Do(func() { go e.drain() })
}

func (e *injectableEndpoint) drain() {
	for {
		select {
		case raw := <-e.base.injected:
			e.base.consumedCount.Add(1)
			e.deliver(raw)
		case <-e.base.done:
			return
		}
	}
}

// deliver 把一个裸 IP 包交给 gvisor 栈；协议号看版本 nibble（与 sing-tun 的做法一致）。
func (e *injectableEndpoint) deliver(raw []byte) {
	if len(raw) < minIPv4PacketLen {
		return
	}
	protocol := header.IPv4ProtocolNumber
	switch raw[0] >> 4 {
	case 4:
	case 6:
		protocol = header.IPv6ProtocolNumber
	default:
		return
	}
	e.mu.RLock()
	dispatcher := e.dispatcher
	e.mu.RUnlock()
	if dispatcher == nil {
		return
	}
	pkt := stack.NewPacketBuffer(stack.PacketBufferOptions{
		Payload:           buffer.MakeWithData(raw),
		IsForwardedPacket: true, // 与本仓库其它注入点（stack_mixed.go）保持一致
	})
	dispatcher.DeliverNetworkPacket(protocol, pkt)
	pkt.DecRef()
}
