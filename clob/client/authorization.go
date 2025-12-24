package client

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"

	"github.com/betbot/gobet/clob/types"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	ethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

// AuthorizationService 对齐 poly-sdk 的 AuthorizationService：
// - 查询 USDC 余额 / allowance
// - 查询 Conditional Tokens(ERC1155) operator approval
// - 一键补齐 approve / setApprovalForAll（用于交易/拆分/合并等）
type AuthorizationService struct {
	client     *ethclient.Client
	privateKey *ecdsa.PrivateKey
	chainID    *big.Int

	usdc              common.Address
	conditionalTokens common.Address

	erc20Spenders    []namedAddress
	erc1155Operators []namedAddress

	erc20ABI   abi.ABI
	erc1155ABI abi.ABI
}

type namedAddress struct {
	Name    string
	Address common.Address
}

type AllowanceInfo struct {
	Contract  string `json:"contract"`
	Address   string `json:"address"`
	Approved  bool   `json:"approved"`
	Allowance string `json:"allowance,omitempty"`
}

type AllowancesResult struct {
	Wallet         string         `json:"wallet"`
	UsdcBalance    string         `json:"usdcBalance"`
	Erc20Allowances []AllowanceInfo `json:"erc20Allowances"`
	Erc1155Approvals []AllowanceInfo `json:"erc1155Approvals"`
	TradingReady   bool           `json:"tradingReady"`
	Issues         []string       `json:"issues"`
}

type ApprovalTxResult struct {
	Contract string `json:"contract"`
	TxHash   string `json:"txHash,omitempty"`
	Success  bool   `json:"success"`
	Error    string `json:"error,omitempty"`
}

type ApprovalsResult struct {
	Wallet          string            `json:"wallet"`
	Erc20Approvals  []ApprovalTxResult `json:"erc20Approvals"`
	Erc1155Approvals []ApprovalTxResult `json:"erc1155Approvals"`
	AllApproved     bool              `json:"allApproved"`
	Summary         string            `json:"summary"`
}

const erc20ABIJSON = `[
  {"inputs":[{"name":"owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
  {"inputs":[{"name":"owner","type":"address"},{"name":"spender","type":"address"}],"name":"allowance","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
  {"inputs":[{"name":"spender","type":"address"},{"name":"amount","type":"uint256"}],"name":"approve","outputs":[{"name":"","type":"bool"}],"stateMutability":"nonpayable","type":"function"}
]`

const erc1155ABIJSON = `[
  {"inputs":[{"name":"operator","type":"address"},{"name":"approved","type":"bool"}],"name":"setApprovalForAll","outputs":[],"stateMutability":"nonpayable","type":"function"},
  {"inputs":[{"name":"account","type":"address"},{"name":"operator","type":"address"}],"name":"isApprovedForAll","outputs":[{"name":"","type":"bool"}],"stateMutability":"view","type":"function"}
]`

func NewAuthorizationService(rpcURL string, chain types.Chain, privateKey *ecdsa.PrivateKey) (*AuthorizationService, error) {
	c, err := ethclient.Dial(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("连接RPC节点失败: %w", err)
	}
	cfg, err := GetContractConfig(chain)
	if err != nil {
		return nil, err
	}

	a20, err := abi.JSON(strings.NewReader(erc20ABIJSON))
	if err != nil {
		return nil, fmt.Errorf("解析ERC20 ABI失败: %w", err)
	}
	a1155, err := abi.JSON(strings.NewReader(erc1155ABIJSON))
	if err != nil {
		return nil, fmt.Errorf("解析ERC1155 ABI失败: %w", err)
	}

	// poly-sdk 口径：USDC 需要 approve 给 Exchange/NegRisk/Adapter/ConditionalTokens 等
	erc20Spenders := []namedAddress{
		{Name: "CTF Exchange", Address: common.HexToAddress(cfg.Exchange)},
		{Name: "Neg Risk CTF Exchange", Address: common.HexToAddress(cfg.NegRiskExchange)},
		{Name: "Neg Risk Adapter", Address: common.HexToAddress(cfg.NegRiskAdapter)},
		{Name: "Conditional Tokens", Address: common.HexToAddress(cfg.ConditionalTokens)},
	}
	erc1155Operators := []namedAddress{
		{Name: "CTF Exchange", Address: common.HexToAddress(cfg.Exchange)},
		{Name: "Neg Risk CTF Exchange", Address: common.HexToAddress(cfg.NegRiskExchange)},
		{Name: "Neg Risk Adapter", Address: common.HexToAddress(cfg.NegRiskAdapter)},
	}

	return &AuthorizationService{
		client:            c,
		privateKey:        privateKey,
		chainID:           big.NewInt(int64(chain)),
		usdc:              common.HexToAddress(cfg.Collateral),
		conditionalTokens: common.HexToAddress(cfg.ConditionalTokens),
		erc20Spenders:     erc20Spenders,
		erc1155Operators:  erc1155Operators,
		erc20ABI:          a20,
		erc1155ABI:        a1155,
	}, nil
}

func (s *AuthorizationService) walletAddress() common.Address {
	return crypto.PubkeyToAddress(s.privateKey.PublicKey)
}

func (s *AuthorizationService) CheckAllowances(ctx context.Context) (*AllowancesResult, error) {
	wallet := s.walletAddress()

	// USDC balance
	balData, err := s.erc20ABI.Pack("balanceOf", wallet)
	if err != nil {
		return nil, err
	}
	balRaw, err := s.client.CallContract(ctx, ethereum.CallMsg{To: &s.usdc, Data: balData}, nil)
	if err != nil {
		return nil, fmt.Errorf("call usdc.balanceOf: %w", err)
	}
	var bal *big.Int
	if err := s.erc20ABI.UnpackIntoInterface(&bal, "balanceOf", balRaw); err != nil {
		return nil, err
	}
	usdcBal := formatUnits6(bal)

	erc20Allowances := make([]AllowanceInfo, 0, len(s.erc20Spenders))
	issues := make([]string, 0, 8)
	for _, sp := range s.erc20Spenders {
		allowData, _ := s.erc20ABI.Pack("allowance", wallet, sp.Address)
		raw, err := s.client.CallContract(ctx, ethereum.CallMsg{To: &s.usdc, Data: allowData}, nil)
		if err != nil {
			return nil, fmt.Errorf("call usdc.allowance(%s): %w", sp.Name, err)
		}
		var allowance *big.Int
		if err := s.erc20ABI.UnpackIntoInterface(&allowance, "allowance", raw); err != nil {
			return nil, err
		}
		approved := isUnlimitedAllowance6(allowance)
		info := AllowanceInfo{
			Contract: sp.Name,
			Address:  sp.Address.Hex(),
			Approved: approved,
		}
		if approved {
			info.Allowance = "unlimited"
		} else {
			info.Allowance = formatUnits6(allowance)
			issues = append(issues, "ERC20: "+sp.Name+" needs USDC approval")
		}
		erc20Allowances = append(erc20Allowances, info)
	}

	erc1155Approvals := make([]AllowanceInfo, 0, len(s.erc1155Operators))
	for _, op := range s.erc1155Operators {
		data, _ := s.erc1155ABI.Pack("isApprovedForAll", wallet, op.Address)
		raw, err := s.client.CallContract(ctx, ethereum.CallMsg{To: &s.conditionalTokens, Data: data}, nil)
		if err != nil {
			return nil, fmt.Errorf("call ctf.isApprovedForAll(%s): %w", op.Name, err)
		}
		var ok bool
		if err := s.erc1155ABI.UnpackIntoInterface(&ok, "isApprovedForAll", raw); err != nil {
			return nil, err
		}
		erc1155Approvals = append(erc1155Approvals, AllowanceInfo{
			Contract: op.Name,
			Address:  op.Address.Hex(),
			Approved: ok,
		})
		if !ok {
			issues = append(issues, "ERC1155: "+op.Name+" needs approval for Conditional Tokens")
		}
	}

	return &AllowancesResult{
		Wallet:          wallet.Hex(),
		UsdcBalance:     usdcBal,
		Erc20Allowances: erc20Allowances,
		Erc1155Approvals: erc1155Approvals,
		TradingReady:    len(issues) == 0,
		Issues:          issues,
	}, nil
}

// ApproveAll 发送链上授权交易：USDC approve + ERC1155 setApprovalForAll。
// 注意：此方法会产生链上交易；调用方应自行做好风控/确认。
func (s *AuthorizationService) ApproveAll(ctx context.Context) (*ApprovalsResult, error) {
	wallet := s.walletAddress()

	status, err := s.CheckAllowances(ctx)
	if err != nil {
		return nil, err
	}

	erc20Results := make([]ApprovalTxResult, 0, len(s.erc20Spenders))
	for i, sp := range s.erc20Spenders {
		if i < len(status.Erc20Allowances) && status.Erc20Allowances[i].Approved {
			erc20Results = append(erc20Results, ApprovalTxResult{Contract: sp.Name, Success: true})
			continue
		}
		txHash, e := s.approveERC20Max(ctx, s.usdc, sp.Address)
		if e != nil {
			erc20Results = append(erc20Results, ApprovalTxResult{Contract: sp.Name, Success: false, Error: e.Error()})
			continue
		}
		erc20Results = append(erc20Results, ApprovalTxResult{Contract: sp.Name, Success: true, TxHash: txHash.Hex()})
	}

	erc1155Results := make([]ApprovalTxResult, 0, len(s.erc1155Operators))
	for _, op := range s.erc1155Operators {
		// 重新查一遍（避免依赖顺序）
		ok, e := s.isApprovedForAll(ctx, wallet, op.Address)
		if e == nil && ok {
			erc1155Results = append(erc1155Results, ApprovalTxResult{Contract: op.Name, Success: true})
			continue
		}
		txHash, e := s.setApprovalForAll(ctx, op.Address, true)
		if e != nil {
			erc1155Results = append(erc1155Results, ApprovalTxResult{Contract: op.Name, Success: false, Error: e.Error()})
			continue
		}
		erc1155Results = append(erc1155Results, ApprovalTxResult{Contract: op.Name, Success: true, TxHash: txHash.Hex()})
	}

	allApproved := true
	for _, r := range erc20Results {
		if !r.Success {
			allApproved = false
			break
		}
	}
	if allApproved {
		for _, r := range erc1155Results {
			if !r.Success {
				allApproved = false
				break
			}
		}
	}

	summary := "approvals completed"
	if allApproved {
		summary = "all approvals set"
	}

	return &ApprovalsResult{
		Wallet:           wallet.Hex(),
		Erc20Approvals:   erc20Results,
		Erc1155Approvals: erc1155Results,
		AllApproved:      allApproved,
		Summary:          summary,
	}, nil
}

func (s *AuthorizationService) isApprovedForAll(ctx context.Context, owner common.Address, operator common.Address) (bool, error) {
	data, err := s.erc1155ABI.Pack("isApprovedForAll", owner, operator)
	if err != nil {
		return false, err
	}
	raw, err := s.client.CallContract(ctx, ethereum.CallMsg{To: &s.conditionalTokens, Data: data}, nil)
	if err != nil {
		return false, err
	}
	var ok bool
	if err := s.erc1155ABI.UnpackIntoInterface(&ok, "isApprovedForAll", raw); err != nil {
		return false, err
	}
	return ok, nil
}

func (s *AuthorizationService) approveERC20Max(ctx context.Context, token common.Address, spender common.Address) (common.Hash, error) {
	max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	data, err := s.erc20ABI.Pack("approve", spender, max)
	if err != nil {
		return common.Hash{}, err
	}
	tx, err := s.buildSignedTx(ctx, token, data, big.NewInt(0))
	if err != nil {
		return common.Hash{}, err
	}
	if err := s.client.SendTransaction(ctx, tx); err != nil {
		return common.Hash{}, err
	}
	return tx.Hash(), nil
}

func (s *AuthorizationService) setApprovalForAll(ctx context.Context, operator common.Address, approved bool) (common.Hash, error) {
	data, err := s.erc1155ABI.Pack("setApprovalForAll", operator, approved)
	if err != nil {
		return common.Hash{}, err
	}
	tx, err := s.buildSignedTx(ctx, s.conditionalTokens, data, big.NewInt(0))
	if err != nil {
		return common.Hash{}, err
	}
	if err := s.client.SendTransaction(ctx, tx); err != nil {
		return common.Hash{}, err
	}
	return tx.Hash(), nil
}

func (s *AuthorizationService) buildSignedTx(ctx context.Context, to common.Address, data []byte, value *big.Int) (*ethtypes.Transaction, error) {
	from := s.walletAddress()
	nonce, err := s.client.PendingNonceAt(ctx, from)
	if err != nil {
		return nil, fmt.Errorf("获取nonce失败: %w", err)
	}
	gasPrice, err := s.client.SuggestGasPrice(ctx)
	if err != nil {
		return nil, fmt.Errorf("获取gas价格失败: %w", err)
	}
	gasLimit, err := s.client.EstimateGas(ctx, ethereum.CallMsg{
		From:  from,
		To:    &to,
		Data:  data,
		Value: value,
	})
	if err != nil {
		// 某些节点对 ERC20 approve 的 EstimateGas 可能不稳定；给一个保守兜底
		gasLimit = 120000
	}
	tx := ethtypes.NewTransaction(nonce, to, value, gasLimit, gasPrice, data)
	signed, err := ethtypes.SignTx(tx, ethtypes.NewEIP155Signer(s.chainID), s.privateKey)
	if err != nil {
		return nil, fmt.Errorf("签名交易失败: %w", err)
	}
	return signed, nil
}

func formatUnits6(v *big.Int) string {
	if v == nil {
		return "0"
	}
	// 格式化为 6 位小数的字符串（不追求极致精度，用于展示/诊断足够）
	whole := new(big.Int).Div(v, big.NewInt(1_000_000))
	frac := new(big.Int).Mod(v, big.NewInt(1_000_000))
	return fmt.Sprintf("%s.%06s", whole.String(), fmt.Sprintf("%06s", frac.String()))
}

func isUnlimitedAllowance6(v *big.Int) bool {
	// poly-sdk 里用 “> 1e12 USDC” 视为 unlimited；这里用 1e18(6 decimals) 做粗门槛
	if v == nil {
		return false
	}
	threshold := new(big.Int).Mul(big.NewInt(1_000_000), big.NewInt(1_000_000_000_000)) // 1e12 * 1e6
	return v.Cmp(threshold) > 0
}

