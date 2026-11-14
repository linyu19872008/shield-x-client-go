package agent

import "runtime/debug"

// ContractVersion 是本 SDK 实现的契约版本。
const ContractVersion = "agent-v1"

// Version 返回 SDK 版本：模块构建信息可用时返回 tag，否则返回 "dev"。
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if info.Main.Version == "" || info.Main.Version == "(devel)" {
		return "dev"
	}
	return info.Main.Version
}
