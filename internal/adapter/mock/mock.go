package mock

import (
	"github.com/bingame/cli-relay/internal/adapter"
	"github.com/bingame/cli-relay/internal/provider"
)

// Adapter 用于进程和 CLI 测试，无须安装或调用真实模型 CLI。
type Adapter struct {
	Name    string
	Inputs  adapter.LaunchInputs
	Resume  []string
	Applied bool
}

func (a *Adapter) Target() string            { return a.Name }
func (a *Adapter) InstallSkill(string) error { return nil }
func (a *Adapter) Render(p provider.Provider, root string, _ ...provider.Model) (adapter.Artifact, error) {
	return adapter.Artifact{Target: a.Name, ProviderID: p.ID}, nil
}
func (a *Adapter) ApplyGlobal(adapter.Artifact, string) error { a.Applied = true; return nil }
func (a *Adapter) BuildLaunchInputs(adapter.Artifact, adapter.ResolvedSecrets) (adapter.LaunchInputs, error) {
	return a.Inputs, nil
}
func (a *Adapter) NativeArgs(_ adapter.Mode, args []string) ([]string, error) { return args, nil }
func (a *Adapter) ResumeArgs(id string) []string                              { return append(append([]string{}, a.Resume...), id) }

var _ adapter.LaunchAdapter = (*Adapter)(nil)
