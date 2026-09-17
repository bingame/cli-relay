package claudecode

import (
	"github.com/bingame/cli-relay/internal/safeio"
	"github.com/bingame/cli-relay/skills"
	"path/filepath"
)

func (*Adapter) InstallSkill(targetDir string) error { return skills.Install(targetDir) }

// 空 setting-sources 会关闭用户 Skill；仅显式加载 Relay 自带插件，保持供应商隔离。
func installHandoffPlugin(targetDir string) error {
	if err := skills.Install(targetDir); err != nil {
		return err
	}
	return safeio.WriteFile(filepath.Join(targetDir, ".claude-plugin", "plugin.json"), []byte("{\"name\":\"relay\",\"skills\":\"./\"}\n"), 0600)
}
