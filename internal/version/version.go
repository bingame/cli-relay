package version

import "runtime/debug"

// 由 GoReleaser 注入；go install 则读取 Go 模块构建信息。
var Version = "dev"

func String() string {
	if Version != "dev" {
		return Version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return Version
}
