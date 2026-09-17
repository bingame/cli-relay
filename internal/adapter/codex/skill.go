package codex

import "github.com/bingame/cli-relay/skills"

func (*Adapter) InstallSkill(targetDir string) error { return skills.Install(targetDir) }
