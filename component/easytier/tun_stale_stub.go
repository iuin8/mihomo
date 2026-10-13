//go:build !darwin

// FORK(easytier-tun): 非 darwin 平台没有这个问题 —— Linux 上 utun 等效设备的生命周期由内核
// 与进程退出共同保证，且 exec 式重启后地址不会变成"多张接口同时持有"。因此这里是 no-op。
package easytier

import "net/netip"

func closeStaleOverlayTunFDs(netip.Addr) int { return 0 }
