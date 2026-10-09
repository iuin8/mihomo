//go:build with_gvisor

package easytier

import (
	"testing"

	"github.com/metacubex/gvisor/pkg/tcpip/stack"
)

// 回归（实测踩到过 ✓）：sing-tun 的 gvisor 栈在 NewGVisor 里对设备做类型断言
//
//	gTun, isGTun := options.Tun.(GVisorTun)   // Tun + WritePacket + NewEndpoint
//
// 包装器只要少了其中一个方法，断言就失败 → 栈报 "gVisor stack is unsupported on current platform"
// → mihomo 把 tun 回写成关闭 → 注入口虽已发布却永远没人读（表现为家侧一直 Timeout）。
func TestWrappedTunSatisfiesGVisorTun(t *testing.T) {
	wrapped, sink := WrapTunForIngress(&recordingTun{incoming: make(chan []byte, 1)})
	if sink == nil {
		t.Fatal("WrapTunForIngress must return an ingress sink")
	}
	if _, ok := wrapped.(interface {
		WritePacket(*stack.PacketBuffer) (int, error)
	}); !ok {
		t.Fatal("wrapped TUN must expose WritePacket, otherwise the gvisor stack refuses to start")
	}
	if _, ok := wrapped.(interface {
		NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error)
	}); !ok {
		t.Fatal("wrapped TUN must expose NewEndpoint, otherwise the gvisor stack refuses to start")
	}
}

// 透传语义：真设备不支持时必须是可读错误，绝不能让栈拿到假成功。
func TestWrappedTunForwardsGVisorCalls(t *testing.T) {
	wrapped, _ := WrapTunForIngress(&recordingTun{incoming: make(chan []byte, 1)})
	if _, err := wrapped.(interface {
		WritePacket(*stack.PacketBuffer) (int, error)
	}).WritePacket(nil); err == nil {
		t.Fatal("WritePacket must report an error when the device does not support it")
	}
	if _, _, err := wrapped.(interface {
		NewEndpoint() (stack.LinkEndpoint, stack.NICOptions, error)
	}).NewEndpoint(); err == nil {
		t.Fatal("NewEndpoint must report an error when the device does not support it")
	}
}
