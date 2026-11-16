package desktop

import (
	"bytes"
	"errors"
	"os"
)

// Electron embeds a fixed sentinel in the framework binary, followed by a
// version byte, a length byte, and one ASCII fuse byte per fuse ('0' disabled,
// '1' enabled). The layout is Electron's own ABI and stable across builds, so a
// binary that does not match is treated as unknown rather than guessed at.
const (
	fuseSentinelPrefix = "dL7pKGdnNz796Pbbj"
	fuseSentinelSuffix = "QWNKmHXBZaB9tsX"

	// fuseLayoutVersion is the only fuse block version this loader understands.
	fuseLayoutVersion = 1
	// fuseNodeInspectorSignal is the index of the fuse that lets SIGUSR1 open the
	// node inspector. Electron 45 layout: RunAsNode, EnableCookieEncryption,
	// EnableNodeOptionsEnvironmentVariable, then this one.
	fuseNodeInspectorSignal = 3
)

// ErrUnknownFuseLayout means the framework binary does not carry a recognisable
// fuse block. The loader stops: sending SIGUSR1 to a build whose signal fuse may
// be off leaves the process without a way back.
var ErrUnknownFuseLayout = errors.New("无法识别 Electron fuse 布局，增强保持停用")

// ErrInspectorDisabled means the build switched the node inspector signal fuse
// off, so SIGUSR1 would never open a port.
var ErrInspectorDisabled = errors.New("当前客户端禁用了 Node inspector 信号，增强无法接入")

// InspectorFuse reports whether the given Electron framework binary allows a
// SIGUSR1-triggered inspector. It is read-only and touches no client state.
func InspectorFuse(binary []byte) error {
	sentinel := []byte(fuseSentinelPrefix + fuseSentinelSuffix)
	at := bytes.Index(binary, sentinel)
	if at < 0 {
		return ErrUnknownFuseLayout
	}
	block := at + len(sentinel)
	if block+2 > len(binary) || binary[block] != fuseLayoutVersion {
		return ErrUnknownFuseLayout
	}
	count := int(binary[block+1])
	if count <= fuseNodeInspectorSignal || block+2+count > len(binary) {
		return ErrUnknownFuseLayout
	}
	fuses := binary[block+2 : block+2+count]
	switch fuses[fuseNodeInspectorSignal] {
	case '1':
		return nil
	case '0':
		return ErrInspectorDisabled
	default:
		return ErrUnknownFuseLayout
	}
}

// CheckInspectorFuse reads the installed framework binary and applies the fuse
// check. A missing binary is reported as an unknown layout so callers degrade
// instead of attaching blind.
func CheckInspectorFuse(framework string) error {
	if framework == "" {
		return ErrUnknownFuseLayout
	}
	data, err := os.ReadFile(framework)
	if err != nil {
		return ErrUnknownFuseLayout
	}
	return InspectorFuse(data)
}
