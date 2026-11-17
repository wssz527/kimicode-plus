package plugins

import "embed"

// Bundled is the plugin set embedded in the binary. Each entry is a full plugin
// directory (manifest, modules, LICENSE) and is released to
// <dataDir>/plugins/<id> on first run. A plugin the user uninstalled stays
// uninstalled: the release step checks its tombstone first.
//
//go:embed all:example-hello all:kct all:monitor
var bundled embed.FS
