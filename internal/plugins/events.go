// Package plugins 预留可选通知与外部编排扩展，不在核心执行流程自动重试或关机。
package plugins

import "context"

type NotificationEvent struct {
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}
type Notifier interface {
	Notify(context.Context, NotificationEvent) error
}
type ExecutionResult struct {
	ExitCode                    int
	Target, Provider, SessionID string
}
type ResultConsumer interface {
	OnResult(context.Context, ExecutionResult) error
}
