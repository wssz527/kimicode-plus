package desktop

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

const (
	fuseSentinelPrefix = "dL7pKGdnNz796Pbbj"
	fuseSentinelSuffix = "QWNKmHXBZaB9tsX"

	// fuseBlockPrefixLen covers the version and count bytes that follow the sentinel.
	fuseBlockPrefixLen = 2
	// fuseMaxCount bounds the declared fuse count. Electron ships a handful of
	// fuses; a larger value means we are not looking at a fuse block.
	fuseMaxCount = 64
)

// FuseLayout binds a fuse block layout to the Electron version verified against
// it. NodeInspectorSignal is the index of the fuse that allows SIGUSR1 to open
// the node inspector. The index is a property of Electron's source order, which
// can change between majors, so it is pinned per verified version: an unlisted
// Electron version fails closed instead of being read at an offset that may now
// point at a different fuse.
type FuseLayout struct {
	Electron string
	// BlockVersion is the layout version byte Electron writes before the fuse
	// strings.
	BlockVersion byte
	// NodeInspectorSignal is the fuse index. Electron 45 order is RunAsNode,
	// EnableCookieEncryption, EnableNodeOptionsEnvironmentVariable,
	// EnableNodeInspectorSignal.
	NodeInspectorSignal int
	// Count is the number of fuse entries in this version's block.
	Count int
}

// VerifiedFuseLayouts is the allowlist of fuse layouts this loader understands.
// A build whose Electron version is absent here is not attached to, even when
// SupportedHosts would accept its app version: the app version alone does not
// say where the inspector signal bit lives.
var VerifiedFuseLayouts = []FuseLayout{
	{Electron: "45.1.0", BlockVersion: 1, NodeInspectorSignal: 3, Count: 9},
}

var (
	// ErrUnknownFuseLayout means the framework binary carries no recognisable
	// fuse block, or carries one that does not match the pinned layout.
	ErrUnknownFuseLayout = errors.New("无法识别 Electron fuse 布局，增强保持停用")
	// ErrInspectorDisabled means the build switched the node inspector signal
	// fuse off, so SIGUSR1 would never open a port.
	ErrInspectorDisabled = errors.New("当前客户端禁用了 Node inspector 信号，增强无法接入")
	// ErrUnverifiedFuseVersion means the Electron version has no pinned fuse
	// layout. Failing closed here is deliberate: guessing the index on a new
	// major could read an unrelated fuse and either block a working build or
	// attach to a broken one.
	ErrUnverifiedFuseVersion = errors.New("该 Electron 版本未经验证 fuse 布局，增强保持停用")
	// ErrFuseBlockTooLarge means the declared fuse count is implausible.
	ErrFuseBlockTooLarge = errors.New("fuse 声明数量超出合理范围")
)

// LayoutFor returns the pinned fuse layout for an Electron version.
func LayoutFor(electron string) (FuseLayout, error) {
	for _, layout := range VerifiedFuseLayouts {
		if layout.Electron == electron {
			return layout, nil
		}
	}
	return FuseLayout{}, ErrUnverifiedFuseVersion
}

// InspectorFuseAt applies a pinned layout to a buffer that contains the fuse
// block. The layout version byte and the declared count must both match, so a
// build whose layout moved is reported instead of misread.
func InspectorFuseAt(binary []byte, layout FuseLayout) error {
	sentinel := []byte(fuseSentinelPrefix + fuseSentinelSuffix)
	at := bytes.Index(binary, sentinel)
	if at < 0 {
		return ErrUnknownFuseLayout
	}
	block := at + len(sentinel)
	if block+fuseBlockPrefixLen > len(binary) {
		return ErrUnknownFuseLayout
	}
	if binary[block] != layout.BlockVersion {
		return ErrUnknownFuseLayout
	}
	count := int(binary[block+1])
	if count > fuseMaxCount {
		return ErrFuseBlockTooLarge
	}
	if count != layout.Count {
		return ErrUnknownFuseLayout
	}
	if layout.NodeInspectorSignal >= count || block+fuseBlockPrefixLen+count > len(binary) {
		return ErrUnknownFuseLayout
	}
	fuses := binary[block+fuseBlockPrefixLen : block+fuseBlockPrefixLen+count]
	switch fuses[layout.NodeInspectorSignal] {
	case '1':
		return nil
	case '0':
		return ErrInspectorDisabled
	default:
		return ErrUnknownFuseLayout
	}
}

// InspectorFuse keeps the historical entry point: it applies the pinned layout of
// the Electron version this loader supports. Callers that know the host's
// Electron version should use CheckInspectorFuseFor so an upgrade fails closed.
func InspectorFuse(binary []byte) error {
	layout, err := LayoutFor(SupportedElectron)
	if err != nil {
		return err
	}
	return InspectorFuseAt(binary, layout)
}

const (
	// fuseChunkSize is the read size used while scanning the framework binary.
	// The framework is roughly 200MB and the sentinel sits in its data section at
	// no documented offset, so the scan streams the file instead of allocating it.
	fuseChunkSize = 1 << 20
	// fuseCarrySize is the tail kept between chunks so a sentinel that straddles a
	// chunk boundary is still complete in the next buffer.
	fuseCarrySize = len(fuseSentinelPrefix) + len(fuseSentinelSuffix) + fuseBlockPrefixLen + fuseMaxCount
)

type fuseCacheEntry struct {
	size    int64
	modTime time.Time
	verdict error
}

var (
	fuseCacheMu sync.Mutex
	fuseCache   = map[string]fuseCacheEntry{}
	// fuseScan is the scanner CheckInspectorFuseFor runs. Tests replace it to
	// count scans and prove the cache actually short-circuits the read.
	fuseScan = scanFuseLayout
)

// CheckInspectorFuseFor verifies the fuse block of an installed framework binary
// for a specific Electron version.
func CheckInspectorFuseFor(framework, electron string) error {
	layout, err := LayoutFor(electron)
	if err != nil {
		return err
	}
	if framework == "" {
		return ErrUnknownFuseLayout
	}
	info, err := os.Stat(framework)
	if err != nil {
		return ErrUnknownFuseLayout
	}
	key := framework + "\x00" + electron
	fuseCacheMu.Lock()
	if cached, ok := fuseCache[key]; ok && cached.size == info.Size() && cached.modTime.Equal(info.ModTime()) {
		fuseCacheMu.Unlock()
		return cached.verdict
	}
	fuseCacheMu.Unlock()
	verdict := fuseScan(framework, layout)
	fuseCacheMu.Lock()
	fuseCache[key] = fuseCacheEntry{size: info.Size(), modTime: info.ModTime(), verdict: verdict}
	fuseCacheMu.Unlock()
	return verdict
}

// CheckInspectorFuse applies the fuse check with the pinned layout of the
// Electron version this loader supports.
func CheckInspectorFuse(framework string) error {
	return CheckInspectorFuseFor(framework, SupportedElectron)
}

// scanFuseLayout streams the binary in fixed-size chunks and applies the pinned
// layout once the sentinel is found. Memory stays bounded by fuseChunkSize
// regardless of the binary's size; the result is cached per file identity, so
// the scan runs once per installed client instead of once per settings refresh.
func scanFuseLayout(path string, layout FuseLayout) error {
	file, err := os.Open(path)
	if err != nil {
		return ErrUnknownFuseLayout
	}
	defer file.Close()
	buffer := make([]byte, fuseChunkSize+fuseCarrySize)
	carry := 0
	for {
		n, readErr := io.ReadFull(file, buffer[carry:carry+fuseChunkSize])
		filled := carry + n
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) && !errors.Is(readErr, io.EOF) {
			return ErrUnknownFuseLayout
		}
		if filled == 0 {
			return ErrUnknownFuseLayout
		}
		verdict := InspectorFuseAt(buffer[:filled], layout)
		if verdict == nil || errors.Is(verdict, ErrInspectorDisabled) || errors.Is(verdict, ErrFuseBlockTooLarge) {
			return verdict
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) || errors.Is(readErr, io.EOF) {
			return ErrUnknownFuseLayout
		}
		carry = fuseCarrySize
		if carry > filled {
			carry = filled
		}
		copy(buffer[:carry], buffer[filled-carry:filled])
	}
}

// resetFuseCache clears the fuse verdict cache. Tests use it so a rewritten
// framework file is re-read.
func resetFuseCache() {
	fuseCacheMu.Lock()
	fuseCache = map[string]fuseCacheEntry{}
	fuseCacheMu.Unlock()
}
