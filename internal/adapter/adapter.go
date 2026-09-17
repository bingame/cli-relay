package adapter

import "github.com/bingame/cli-relay/internal/provider"

type Mode string

const (
	Interactive Mode = "run"
	Headless    Mode = "exec"
)

// Artifact 不含明文密钥；Path 指向可长期保留的原生配置产物。
type Artifact struct {
	Target     string
	ProviderID string
	Path       string
	Content    []byte
	EnvKey     string
	Config     map[string]any
}
type ResolvedSecrets map[string]string
type LaunchInputs struct {
	Binary   string
	Args     []string
	Env      map[string]string
	UnsetEnv []string
}
type LaunchAdapter interface {
	Target() string
	Render(provider.Provider, string) (Artifact, error)
	ApplyGlobal(Artifact, string) error
	BuildLaunchInputs(Artifact, ResolvedSecrets) (LaunchInputs, error)
	NativeArgs(Mode, []string) ([]string, error)
	ResumeArgs(string) []string
	InstallSkill(targetDir string) error
}
