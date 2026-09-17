package cli

import (
	"github.com/spf13/cobra"
	"relay/internal/process"
)

func (a *App) statusCommand() *cobra.Command {
	return &cobra.Command{Use: "status", Short: "显示各 CLI 默认供应商和仍在运行的实例", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		current, e := a.current()
		if e != nil {
			return e
		}
		instances, e := process.Instances(a.Home)
		if e != nil {
			return e
		}
		return outputJSON(cmd, map[string]any{"current": current, "instances": instances})
	}}
}
