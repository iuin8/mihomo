//go:build !with_gvisor

// FORK(easytier-tun): 无 gvisor 构建下的包装器。
// 这时的栈是 system/mipstack，只需要 tun.Tun（Read/Write/Close），无需额外方法透传。
package easytier

import tun "github.com/metacubex/sing-tun"

type injectableTun struct {
	*injectableTunBase
}

// WrapTunForIngress 包装宿主 TUN。返回值交给 sing-tun 的栈使用，第二个返回值是注入口。
func WrapTunForIngress(device tun.Tun) (tun.Tun, IngressSink) {
	wrapped := &injectableTun{injectableTunBase: newInjectableTunBase(device)}
	return wrapped, wrapped
}
