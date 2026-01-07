package dashboard

// Options 控制 dashboard 的策略级别参数（标题/日志文件名等）。
// 目的：把“策略名/日志文件名/标题”从实现里抽出来，供多个策略复用同一套 UI。
type Options struct {
	// StrategyID 用于日志文件名等（例如 "velocityfollow", "winbet"）
	StrategyID string
	// Title UI 标题（header 左侧展示）
	Title string
}

