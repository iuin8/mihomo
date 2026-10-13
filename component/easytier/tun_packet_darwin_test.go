//go:build darwin

package easytier

import (
	"bytes"
	"testing"

	"golang.org/x/sys/unix"
)

// headerTestDevice 记录被写入的字节，并按脚本返回下一次 Read 的内容。
type headerTestDevice struct {
	written []byte
	reads   [][]byte
}

func (f *headerTestDevice) Read(p []byte) (int, error) {
	if len(f.reads) == 0 {
		return 0, nil
	}
	next := f.reads[0]
	f.reads = f.reads[1:]
	return copy(p, next), nil
}

func (f *headerTestDevice) Write(p []byte) (int, error) {
	f.written = append([]byte(nil), p...)
	return len(p), nil
}

func (f *headerTestDevice) Close() error { return nil }

// 契约：写出去必须带 4 字节地址族头（否则 macOS 内核整包丢弃 ✗✓），读回来必须剥掉它。
func TestPacketHeaderDevice(t *testing.T) {
	packet := []byte{0x45, 0x00, 0x00, 0x3c, 0x01, 0x02, 0x03, 0x04}

	device := &headerTestDevice{}
	wrapped := wrapPacketDevice(device)
	if _, err := wrapped.Write(packet); err != nil {
		t.Fatalf("write: %v", err)
	}
	want := append([]byte{0x00, 0x00, 0x00, unix.AF_INET}, packet...)
	if !bytes.Equal(device.written, want) {
		t.Fatalf("write must prepend the utun address-family header\n got %v\nwant %v", device.written, want)
	}

	device.reads = [][]byte{append([]byte{0x00, 0x00, 0x00, unix.AF_INET}, packet...)}
	buffer := make([]byte, 64)
	length, err := wrapped.Read(buffer)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buffer[:length], packet) {
		t.Fatalf("read must strip the header, got %v", buffer[:length])
	}

	// 非 IPv4(选项头/其它形状)原样返回，交给上层判定 —— 不能在这里把包悄悄吃掉 ✗。
	odd := []byte{0xff, 0xff, 0xff, 0xff, 0x45, 0x00}
	device.reads = [][]byte{odd}
	length, err = wrapped.Read(buffer)
	if err != nil || length != len(odd) {
		t.Fatalf("non-header read must pass through: len=%d err=%v", length, err)
	}
}
