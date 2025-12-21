package unifiedarb

import "fmt"

// Config：统一套利策略（融合 complete-set + pairlock 风控 + 分阶段执行）
//
// 设计目标：
// - **核心套利**：当 YES_ask + NO_ask <= 100 - ProfitTargetCents 时，买入等量 YES+NO（complete-set），锁定到期收益
// - **执行框架**：所有下单统一走 TradingService.ExecuteMultiLeg（多腿并发 + in-flight 去重 + 自动对冲）
// - **新架构**：通过 session 的 PriceChanged + OrderUpdate 驱动策略内部状态机（loop）
// - **融合点**：
//   - arbitrage/pairedtrading：complete-set 机会识别 + 冷却/轮数控制
//   - pairlock：更强的失败动作与并行控制（保守实现，避免扩大裸露）
//   - pairedtrading README：分阶段（Build/Lock/Amplify）调度（默认只做“锁定型放大利润”，不做方向性押注）
type Config struct {
	// ----- 基础参数（兼容 arbitrage/pairedtrading 简化版） -----
	OrderSize          float64 `json:"orderSize" yaml:"orderSize"`
	MinOrderSize       float64 `json:"minOrderSize" yaml:"minOrderSize"`
	ProfitTargetCents  int     `json:"profitTargetCents" yaml:"profitTargetCents"`
	MaxRoundsPerPeriod int     `json:"maxRoundsPerPeriod" yaml:"maxRoundsPerPeriod"`
	CooldownMs         int     `json:"cooldownMs" yaml:"cooldownMs"`

	// ----- 分阶段（pairedtrading 核心思路：建仓 -> 锁定 -> 放大） -----
	// 若未配置/为 0：默认仅启用 Lock（即纯 complete-set 机会）
	CycleDurationSeconds int     `json:"cycleDurationSeconds" yaml:"cycleDurationSeconds"`
	BuildDurationSeconds int     `json:"buildDurationSeconds" yaml:"buildDurationSeconds"`
	AmplifyStartSeconds  int     `json:"amplifyStartSeconds" yaml:"amplifyStartSeconds"`
	EarlyLockPrice       float64 `json:"earlyLockPrice" yaml:"earlyLockPrice"`       // 0.85
	EarlyAmplifyPrice    float64 `json:"earlyAmplifyPrice" yaml:"earlyAmplifyPrice"` // 0.90

	// Build：在低价区用小额/多笔建立基础仓位（可选）
	BaseTarget     float64 `json:"baseTarget" yaml:"baseTarget"`         // 每边目标 shares
	BuildLotSize   float64 `json:"buildLotSize" yaml:"buildLotSize"`     // 单次建仓 shares
	BuildThreshold float64 `json:"buildThreshold" yaml:"buildThreshold"` // 价格上限（decimal，例如 0.60）
	MinRatio       float64 `json:"minRatio" yaml:"minRatio"`             // 默认 0.40
	MaxRatio       float64 `json:"maxRatio" yaml:"maxRatio"`             // 默认 0.60

	// Lock/Amplify：目标是提高“最差情形收益”（min(P_up_win, P_down_win)）
	TargetProfitBase float64 `json:"targetProfitBase" yaml:"targetProfitBase"` // USDC
	AmplifyTarget    float64 `json:"amplifyTarget" yaml:"amplifyTarget"`       // USDC

	// Lock 阶段（风险敞口驱动）
	LockThreshold float64 `json:"lockThreshold" yaml:"lockThreshold"` // USDC：某方向亏损超过该值时优先修复
	LockPriceMax  float64 `json:"lockPriceMax" yaml:"lockPriceMax"`   // decimal：锁定阶段最高买入价格（默认 0.70）
	ExtremeHigh   float64 `json:"extremeHigh" yaml:"extremeHigh"`     // decimal：极端价格阈值（默认 0.80）
	InsuranceSize float64 `json:"insuranceSize" yaml:"insuranceSize"` // shares：极端价格时买入反向保险（默认 1.5）

	// Amplify 阶段（方向性放大 + 反向保险）
	AmplifyPriceMax    float64 `json:"amplifyPriceMax" yaml:"amplifyPriceMax"`       // decimal：放大阶段最高买入价格（默认 0.85）
	InsurancePriceMax  float64 `json:"insurancePriceMax" yaml:"insurancePriceMax"`   // decimal：反向保险最高价格（默认 0.20）
	DirectionThreshold float64 `json:"directionThreshold" yaml:"directionThreshold"` // decimal：主方向判定阈值（默认 0.70）

	// ----- pairlock 风控（保守实现） -----
	EnableParallel     bool `json:"enableParallel" yaml:"enableParallel"`
	MaxConcurrentPlans int  `json:"maxConcurrentPlans" yaml:"maxConcurrentPlans"`
	// MaxTotalUnhedgedShares 全局在途“最坏未对冲规模”预算（shares）。
	// 用于并行模式：避免同时启动过多计划导致最坏情况下（单腿成交）裸露累积过大。
	// 默认（EnableParallel=true 且未显式配置时）：等于 OrderSize。
	MaxTotalUnhedgedShares   float64 `json:"maxTotalUnhedgedShares" yaml:"maxTotalUnhedgedShares"`
	MaxPlanAgeSeconds        int     `json:"maxPlanAgeSeconds" yaml:"maxPlanAgeSeconds"`
	OnFailAction             string  `json:"onFailAction" yaml:"onFailAction"` // pause/cancel_pause/flatten_pause
	FailMaxSellSlippageCents int     `json:"failMaxSellSlippageCents" yaml:"failMaxSellSlippageCents"`
	FailFlattenMinShares     float64 `json:"failFlattenMinShares" yaml:"failFlattenMinShares"`
	EntryMaxBuySlippageCents int     `json:"entryMaxBuySlippageCents" yaml:"entryMaxBuySlippageCents"`

	// ----- 自动对冲（交给 ExecutionEngine；策略仅做参数透传） -----
	HedgeEnabled              bool    `json:"hedgeEnabled" yaml:"hedgeEnabled"`
	HedgeDelaySeconds         int     `json:"hedgeDelaySeconds" yaml:"hedgeDelaySeconds"`
	HedgeSellPriceOffsetCents int     `json:"hedgeSellPriceOffsetCents" yaml:"hedgeSellPriceOffsetCents"`
	MinExposureToHedge        float64 `json:"minExposureToHedge" yaml:"minExposureToHedge"`
}

func (c *Config) Validate() error {
	if c.OrderSize <= 0 {
		return fmt.Errorf("orderSize 必须 > 0")
	}
	if c.MinOrderSize <= 0 {
		c.MinOrderSize = 1.1
	}
	if c.MinOrderSize < 1.0 {
		return fmt.Errorf("minOrderSize 必须 >= 1.0 USDC（交易所要求）")
	}
	if c.ProfitTargetCents < 0 || c.ProfitTargetCents > 100 {
		return fmt.Errorf("profitTargetCents 必须在 [0,100] 范围内")
	}
	if c.MaxRoundsPerPeriod <= 0 {
		c.MaxRoundsPerPeriod = 1
	}
	if c.CooldownMs <= 0 {
		c.CooldownMs = 250
	}

	// 分阶段默认值：不配置则视为仅 Lock（不做 Build/Amplify 的“额外调度”）
	if c.CycleDurationSeconds < 0 || c.BuildDurationSeconds < 0 || c.AmplifyStartSeconds < 0 {
		return fmt.Errorf("cycle/build/amplify 的 seconds 不能为负数")
	}
	if c.CycleDurationSeconds > 0 {
		if c.BuildDurationSeconds == 0 {
			c.BuildDurationSeconds = c.CycleDurationSeconds / 3
		}
		if c.AmplifyStartSeconds == 0 {
			c.AmplifyStartSeconds = c.CycleDurationSeconds * 2 / 3
		}
		if c.BuildDurationSeconds > c.CycleDurationSeconds {
			c.BuildDurationSeconds = c.CycleDurationSeconds
		}
		if c.AmplifyStartSeconds > c.CycleDurationSeconds {
			c.AmplifyStartSeconds = c.CycleDurationSeconds
		}
	}
	if c.EarlyLockPrice < 0 {
		return fmt.Errorf("earlyLockPrice 不能为负数")
	}
	if c.EarlyAmplifyPrice < 0 {
		return fmt.Errorf("earlyAmplifyPrice 不能为负数")
	}
	if c.CycleDurationSeconds > 0 {
		if c.EarlyLockPrice == 0 {
			c.EarlyLockPrice = 0.85
		}
		if c.EarlyAmplifyPrice == 0 {
			c.EarlyAmplifyPrice = 0.90
		}
	}
	if c.BuildThreshold < 0 {
		return fmt.Errorf("buildThreshold 不能为负数")
	}
	if c.BaseTarget < 0 || c.BuildLotSize < 0 {
		return fmt.Errorf("baseTarget/buildLotSize 不能为负数")
	}
	if c.MinRatio < 0 || c.MaxRatio < 0 {
		return fmt.Errorf("minRatio/maxRatio 不能为负数")
	}
	if c.MinRatio == 0 {
		c.MinRatio = 0.40
	}
	if c.MaxRatio == 0 {
		c.MaxRatio = 0.60
	}
	if c.MinRatio > c.MaxRatio {
		return fmt.Errorf("minRatio 不能大于 maxRatio")
	}
	if c.MinRatio > 1.0 || c.MaxRatio > 1.0 {
		return fmt.Errorf("minRatio/maxRatio 不能大于 1.0")
	}
	if c.TargetProfitBase < 0 || c.AmplifyTarget < 0 {
		return fmt.Errorf("targetProfitBase/amplifyTarget 不能为负数")
	}
	if c.LockThreshold < 0 {
		return fmt.Errorf("lockThreshold 不能为负数")
	}
	if c.LockThreshold == 0 {
		c.LockThreshold = 5.0
	}
	if c.LockPriceMax < 0 || c.ExtremeHigh < 0 || c.AmplifyPriceMax < 0 || c.InsurancePriceMax < 0 || c.DirectionThreshold < 0 {
		return fmt.Errorf("价格阈值不能为负数")
	}
	if c.LockPriceMax == 0 {
		c.LockPriceMax = 0.70
	}
	if c.ExtremeHigh == 0 {
		c.ExtremeHigh = 0.80
	}
	if c.InsuranceSize < 0 {
		return fmt.Errorf("insuranceSize 不能为负数")
	}
	if c.InsuranceSize == 0 {
		c.InsuranceSize = 1.5
	}
	if c.AmplifyPriceMax == 0 {
		c.AmplifyPriceMax = 0.85
	}
	if c.InsurancePriceMax == 0 {
		c.InsurancePriceMax = 0.20
	}
	if c.DirectionThreshold == 0 {
		c.DirectionThreshold = 0.70
	}
	if c.LockPriceMax > 1.0 || c.ExtremeHigh > 1.0 || c.AmplifyPriceMax > 1.0 || c.InsurancePriceMax > 1.0 || c.DirectionThreshold > 1.0 {
		return fmt.Errorf("价格阈值不能大于 1.0")
	}

	// 并行/失败动作
	if !c.EnableParallel {
		c.MaxConcurrentPlans = 1
		// 串行模式下风险预算不生效，保持为 0 以表达“无需预算”
		if c.MaxTotalUnhedgedShares < 0 {
			return fmt.Errorf("maxTotalUnhedgedShares 不能为负数")
		}
		c.MaxTotalUnhedgedShares = 0
	}
	if c.EnableParallel && c.MaxConcurrentPlans <= 0 {
		c.MaxConcurrentPlans = 2
	}
	if c.EnableParallel {
		if c.MaxTotalUnhedgedShares <= 0 {
			c.MaxTotalUnhedgedShares = c.OrderSize
		}
		if c.MaxTotalUnhedgedShares < 0 {
			return fmt.Errorf("maxTotalUnhedgedShares 不能为负数")
		}
	}
	if c.MaxPlanAgeSeconds <= 0 {
		c.MaxPlanAgeSeconds = 60
	}
	if c.OnFailAction == "" {
		c.OnFailAction = "pause"
	}
	switch c.OnFailAction {
	case "pause", "cancel_pause", "flatten_pause":
	default:
		return fmt.Errorf("onFailAction 无效: %s (允许: pause/cancel_pause/flatten_pause)", c.OnFailAction)
	}
	if c.FailMaxSellSlippageCents < 0 {
		return fmt.Errorf("failMaxSellSlippageCents 不能为负数")
	}
	if c.FailFlattenMinShares < 0 {
		return fmt.Errorf("failFlattenMinShares 不能为负数")
	}
	if c.FailFlattenMinShares == 0 {
		c.FailFlattenMinShares = 1.0
	}
	if c.EntryMaxBuySlippageCents < 0 {
		return fmt.Errorf("entryMaxBuySlippageCents 不能为负数")
	}

	// 对冲参数默认值
	// 说明：为了与现有 arbitrage/pairlock 一致，默认开启自动对冲
	if !c.HedgeEnabled {
		// allow explicit disable
	} else {
		// when not explicitly set by user, keep enabled in strategy defaults (see strategy.Initialize)
	}
	if c.HedgeDelaySeconds < 0 {
		return fmt.Errorf("hedgeDelaySeconds 不能为负数")
	}
	if c.HedgeSellPriceOffsetCents < 0 {
		return fmt.Errorf("hedgeSellPriceOffsetCents 不能为负数")
	}
	if c.MinExposureToHedge < 0 {
		return fmt.Errorf("minExposureToHedge 不能为负数")
	}
	return nil
}
