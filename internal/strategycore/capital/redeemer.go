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

type Redeemer struct {
	tradingService *services.TradingService
	config         ConfigInterface

	mu               sync.Mutex
	submittedRedeems map[string]time.Time
}

func NewRedeemer(ts *services.TradingService, cfg ConfigInterface) *Redeemer {
	return &Redeemer{
		tradingService:   ts,
		config:           cfg,
		submittedRedeems: make(map[string]time.Time),
	}
}

func (r *Redeemer) RedeemSettledPositions(ctx context.Context) error {
	_ = ctx
	if r.tradingService == nil {
		return fmt.Errorf("TradingService 未初始化")
	}

	r.mu.Lock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for key, submittedAt := range r.submittedRedeems {
		if submittedAt.Before(cutoff) {
			delete(r.submittedRedeems, key)
		}
	}
	r.mu.Unlock()

	rLog.Infof("🔄 [Redeemer] 开始检查已结算持仓")
	return nil
}

func (r *Redeemer) redeemPosition(ctx context.Context, conditionID string, outcomeIndex int) error {
	_ = ctx
	key := fmt.Sprintf("%s-%d", conditionID, outcomeIndex)
	r.mu.Lock()
	if _, exists := r.submittedRedeems[key]; exists {
		r.mu.Unlock()
		rLog.Debugf("⏸️ [Redeemer] 已提交过赎回: conditionID=%s outcomeIndex=%d", conditionID, outcomeIndex)
		return nil
	}
	r.mu.Unlock()

	conditionHash := common.HexToHash(conditionID)
	indexSet := big.NewInt(1)
	if outcomeIndex == 1 {
		indexSet = big.NewInt(2)
	}

	apiTx, err := api.BuildRedeemTransaction(conditionHash, indexSet)
	if err != nil {
		return fmt.Errorf("构建赎回交易失败: %w", err)
	}

	_ = relayertypes.SafeTransaction{
		To:        apiTx.To.Hex(),
		Operation: relayertypes.OperationType(apiTx.Operation),
		Data:      "0x" + hex.EncodeToString(apiTx.Data),
		Value:     apiTx.Value.String(),
	}

	r.mu.Lock()
	r.submittedRedeems[key] = time.Now()
	r.mu.Unlock()

	rLog.Infof("✅ [Redeemer] 赎回交易已构建: conditionID=%s outcomeIndex=%d", conditionID, outcomeIndex)
	return nil
}

