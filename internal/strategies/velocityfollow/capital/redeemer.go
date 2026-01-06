package capital

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/betbot/gobet/internal/services"
	"github.com/betbot/gobet/pkg/sdk/api"
	relayertypes "github.com/betbot/gobet/pkg/sdk/relayer/types"
	"github.com/ethereum/go-ethereum/common"
	"github.com/sirupsen/logrus"
)

var rLog = logrus.WithField("module", "redeemer")

// Redeemer 赎回逻辑
type Redeemer struct {
	tradingService *services.TradingService
	config         ConfigInterface

	mu              sync.Mutex
	submittedRedeems map[string]time.Time // conditionID-outcome -> submittedAt
}

// NewRedeemer 创建新的赎回器
func NewRedeemer(ts *services.TradingService, cfg ConfigInterface) *Redeemer {
	return &Redeemer{
		tradingService:   ts,
		config:           cfg,
		submittedRedeems: make(map[string]time.Time),
	}
}

// RedeemSettledPositions 赎回已结算的持仓
func (r *Redeemer) RedeemSettledPositions(ctx context.Context) error {
	if r.tradingService == nil {
		return fmt.Errorf("TradingService 未初始化")
	}

	// 清理旧的提交记录（超过 10 分钟）
	r.mu.Lock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for key, submittedAt := range r.submittedRedeems {
		if submittedAt.Before(cutoff) {
			delete(r.submittedRedeems, key)
		}
	}
	r.mu.Unlock()

	// TODO: 获取已结算的持仓
	// 这里需要调用 Data API 或通过其他方式获取持仓信息
	// 由于需要访问 Data API，这里先提供一个框架

	rLog.Infof("🔄 [Redeemer] 开始检查已结算持仓")

	// 示例：假设我们有一个方法可以获取已结算的持仓
	// settledPositions := r.getSettledPositions(ctx)
	// for _, pos := range settledPositions {
	//     if err := r.redeemPosition(ctx, pos); err != nil {
	//         log.Warnf("⚠️ [Redeemer] 赎回失败: %v", err)
	//     }
	// }

	return nil
}

// redeemPosition 赎回单个持仓
func (r *Redeemer) redeemPosition(ctx context.Context, conditionID string, outcomeIndex int) error {
	// 检查是否已提交
	key := fmt.Sprintf("%s-%d", conditionID, outcomeIndex)
	r.mu.Lock()
	if _, exists := r.submittedRedeems[key]; exists {
		r.mu.Unlock()
		rLog.Debugf("⏸️ [Redeemer] 已提交过赎回: conditionID=%s outcomeIndex=%d", conditionID, outcomeIndex)
		return nil
	}
	r.mu.Unlock()

	// 转换 condition ID
	conditionHash := common.HexToHash(conditionID)

	// 确定 index set
	indexSet := big.NewInt(1)
	if outcomeIndex == 1 {
		indexSet = big.NewInt(2)
	}

	// 构建赎回交易
	apiTx, err := api.BuildRedeemTransaction(conditionHash, indexSet)
	if err != nil {
		return fmt.Errorf("构建赎回交易失败: %w", err)
	}

	// 转换为 Relayer 交易
	// 注意：api.SafeTransaction 的 Data 是 []byte，需要转换为 hex 字符串
	_ = relayertypes.SafeTransaction{
		To:        apiTx.To.Hex(),
		Operation: relayertypes.OperationType(apiTx.Operation),
		Data:      "0x" + hex.EncodeToString(apiTx.Data),
		Value:     apiTx.Value.String(),
	}

	// TODO: 获取 Relayer 客户端并执行赎回
	// 这里需要从 TradingService 或配置中获取 Relayer 客户端
	// relayerClient := r.getRelayerClient()
	// authOption := r.getAuthOption()
	// resp, err := relayerClient.Execute([]relayertypes.SafeTransaction{relayerTx}, metadata, authOption)

	// 记录已提交
	r.mu.Lock()
	r.submittedRedeems[key] = time.Now()
	r.mu.Unlock()

	rLog.Infof("✅ [Redeemer] 赎回交易已构建: conditionID=%s outcomeIndex=%d", conditionID, outcomeIndex)

	return nil
}
