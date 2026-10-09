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
	return creator.NewEndpoint()
}
