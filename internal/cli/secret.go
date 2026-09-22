package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (a *App) secretCommand() *cobra.Command {
	parent := &cobra.Command{Use: "secret", Short: "供原生 CLI 回调读取凭据"}
	get := &cobra.Command{
		Use:   "get <cli> <provider>",
		Short: "仅向标准输出写入供应商密钥",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.adapter(args[0]); err != nil {
				return err
			}
			unlock, err := a.lock(cmd.Context())
			if err != nil {
				return err
			}
			defer unlock()
			store, err := a.store(cmd, true)
			if err != nil {
				return err
			}
			defer store.Close()
			p, err := a.resolveProvider(cmd.Context(), store, args[0], args[1])
			if err != nil {
				return err
			}
			if !p.Supports(args[0]) {
				return fmt.Errorf("供应商不支持指定 CLI")
			}
			if p.EffectiveStatus() == "disabled" {
				return fmt.Errorf("供应商已被 cc-switch 同步标记为失效")
			}
			values, err := store.Secrets(cmd.Context(), p.Target, p.ID)
			if err != nil {
				return err
			}
			key := values["api_key"]
			if key == "" {
				return fmt.Errorf("供应商未配置 API key")
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), key)
			return err
		},
	}
	get.ValidArgsFunction = a.completeProviderArg()
	parent.AddCommand(get)
	return parent
}
