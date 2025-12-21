package bbgo

// ExecutionMode 决定策略的“命令/下单”执行语义：
// - Deterministic：确定性（串行），适合网格等需要严格顺序的策略
// - Concurrent：并发（有界），适合套利等需要吞吐的策略
type ExecutionMode string

const (
	ExecutionModeDeterministic ExecutionMode = "deterministic"
	ExecutionModeConcurrent    ExecutionMode = "concurrent"
)

// ExecutionModeProvider 可选接口：策略实现该接口即可声明自己的执行模式。
// 未实现时默认 deterministic。
type ExecutionModeProvider interface {
	ExecutionMode() ExecutionMode
}
