// Package syncer provides auto-redemption for resolved Polymarket positions.
// Uses Polymarket's Relayer API for gasless transactions via Safe wallets.
package redeem

import (
	"context"
	"crypto/ecdsa"
	"encoding/hex"
	"fmt"
	"github.com/betbot/gobet/pkg/sdk/api"
	"github.com/betbot/gobet/pkg/sdk/relayer"
	"github.com/betbot/gobet/pkg/sdk/relayer/types"
	sdktypes "github.com/betbot/gobet/pkg/sdk/types"
	"log"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

const (
	// Delay between individual redeem calls - relayer limit is 15/min, so 5s = 12/min max (safe)
	redeemDelay = 5 * time.Second
	// Max redeems per check cycle to stay under daily quota
	maxRedeemsPerCycle = 50
)

// AutoRedeemer automatically redeems resolved positions via Polymarket Relayer
type AutoRedeemer struct {
	client        *api.Client
	relayerClient *relayer.Client
	safeAddr      common.Address
	eoaAddr       common.Address
	privateKey    *ecdsa.PrivateKey

	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup

	// Track submitted redeems to avoid duplicates
	submittedRedeems   map[string]time.Time
	submittedRedeemsMu sync.RWMutex

	// Stats
	totalRedeemed  int
	totalUSDC      float64
	lastRedeemTime time.Time
	statsMu        sync.RWMutex
}

// NewAutoRedeemer creates a new auto-redeemer using the Relayer API
func NewAutoRedeemer(apiClient *api.Client) (*AutoRedeemer, error) {
	// Get private key from env
	pkHex := strings.TrimSpace(os.Getenv("POLYMARKET_PRIVATE_KEY"))
	if pkHex == "" {
		return nil, fmt.Errorf("POLYMARKET_PRIVATE_KEY not set")
	}
	pkHex = strings.TrimPrefix(pkHex, "0x")

	privateKey, err := crypto.HexToECDSA(pkHex)
	if err != nil {
		return nil, fmt.Errorf("invalid private key: %w", err)
	}

	publicKey := privateKey.Public()
	publicKeyECDSA, ok := publicKey.(*ecdsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("error casting public key")
	}
	eoaAddr := crypto.PubkeyToAddress(*publicKeyECDSA)

	// Get Safe/funder address (Magic wallet)
	funderAddr := strings.TrimSpace(os.Getenv("POLYMARKET_FUNDER_ADDRESS"))
	var safeAddr common.Address
	if funderAddr != "" {
		safeAddr = common.HexToAddress(funderAddr)
	} else {
		// If no funder, use EOA as Safe (for EOA-only wallets)
		safeAddr = eoaAddr
	}

	// Get Builder credentials
	builderKey := strings.TrimSpace(os.Getenv("BUILDER_API_KEY"))
	builderSecret := strings.TrimSpace(os.Getenv("BUILDER_SECRET"))
	builderPassphrase := strings.TrimSpace(os.Getenv("BUILDER_PASS_PHRASE"))

	if builderKey == "" || builderSecret == "" || builderPassphrase == "" {
		return nil, fmt.Errorf("BUILDER_API_KEY, BUILDER_SECRET, and BUILDER_PASS_PHRASE are required for auto-redeem")
	}

	// Create signature function
	signFn := func(signer string, digest []byte) ([]byte, error) {
		sig, err := crypto.Sign(digest, privateKey)
		if err != nil {
			return nil, err
		}
		// Adjust v value for Ethereum (add 27)
		if sig[64] < 27 {
			sig[64] += 27
		}
		return sig, nil
	}

	// Get relayer URL from env or use default
	relayerURL := strings.TrimSpace(os.Getenv("POLYMARKET_RELAYER_URL"))
	if relayerURL == "" {
		relayerURL = "https://relayer-v2.polymarket.com"
	}

	// Create relayer client
	builderCreds := &sdktypes.BuilderApiKeyCreds{
		Key:        builderKey,
		Secret:     builderSecret,
		Passphrase: builderPassphrase,
	}

	chainID := big.NewInt(137) // Polygon
	relayerClient := relayer.NewClient(relayerURL, chainID, signFn, builderCreds)

	log.Printf("[AutoRedeemer] Initialized with Relayer API for Safe wallet %s (signer: %s)",
		safeAddr.Hex(), eoaAddr.Hex())

	return &AutoRedeemer{
		client:           apiClient,
		relayerClient:    relayerClient,
		safeAddr:         safeAddr,
		eoaAddr:          eoaAddr,
		privateKey:       privateKey,
		stopCh:           make(chan struct{}),
		submittedRedeems: make(map[string]time.Time),
	}, nil
}

// Start begins the auto-redeem loop
func (ar *AutoRedeemer) Start(ctx context.Context) error {
	if ar.running {
		return fmt.Errorf("auto-redeemer already running")
	}

	ar.running = true
	ar.wg.Add(1)
	go ar.redeemLoop(ctx)

	// Check if Safe is deployed asynchronously (non-blocking)
	go func() {
		safeAddrStr := ar.safeAddr.Hex()
		deployed, err := ar.relayerClient.GetDeployed(safeAddrStr)
		if err != nil {
			log.Printf("[AutoRedeemer] Warning: could not check Safe deployment: %v", err)
		} else if !deployed.Deployed {
			log.Printf("[AutoRedeemer] Warning: Safe wallet not deployed yet")
		} else {
			log.Printf("[AutoRedeemer] Safe wallet is deployed")
		}
	}()

	log.Printf("[AutoRedeemer] Started - runs every 3 minutes (immediate run on startup, gasless via Relayer)")
	return nil
}

// Stop halts the auto-redeemer
func (ar *AutoRedeemer) Stop() {
	if !ar.running {
		return
	}
	log.Println("[AutoRedeemer] Stopping...")
	ar.running = false
	close(ar.stopCh)

	// Wait for goroutines with timeout
	done := make(chan struct{})
	go func() {
		ar.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Printf("[AutoRedeemer] Stopped - total redeemed: %d positions, $%.2f USDC", ar.totalRedeemed, ar.totalUSDC)
	case <-time.After(3 * time.Second):
		log.Printf("[AutoRedeemer] Stop timeout after 3s - forcing shutdown (total redeemed: %d positions, $%.2f USDC)", ar.totalRedeemed, ar.totalUSDC)
	}
}

// GetStats returns redemption statistics
func (ar *AutoRedeemer) GetStats() (redeemed int, usdc float64, lastTime time.Time) {
	ar.statsMu.RLock()
	defer ar.statsMu.RUnlock()
	return ar.totalRedeemed, ar.totalUSDC, ar.lastRedeemTime
}

func (ar *AutoRedeemer) redeemLoop(ctx context.Context) {
	defer ar.wg.Done()

	// Execute immediately on startup
	log.Printf("[AutoRedeemer] 🚀 Initial redemption run starting...")
	ar.checkAndRedeem(ctx)
	log.Printf("[AutoRedeemer] Initial redemption run completed")

	// Ticker for 3-minute interval executions
	ticker := time.NewTicker(3 * time.Minute)
	defer ticker.Stop()

	for {
		log.Printf("[AutoRedeemer] Next redemption run scheduled in 3 minutes")

		// Calculate next execution time
		select {
		case <-ctx.Done():
			return
		case <-ar.stopCh:
			return
		case <-ticker.C:
			log.Printf("[AutoRedeemer] 🔄 Redemption run starting (3-minute interval)")
			ar.checkAndRedeem(ctx)
		}
	}
}

func (ar *AutoRedeemer) checkAndRedeem(ctx context.Context) {
	// Clean up old submitted entries (older than 10 minutes)
	ar.submittedRedeemsMu.Lock()
	cutoff := time.Now().Add(-10 * time.Minute)
	for key, submittedAt := range ar.submittedRedeems {
		if submittedAt.Before(cutoff) {
			delete(ar.submittedRedeems, key)
		}
	}
	ar.submittedRedeemsMu.Unlock()

	// Get open positions
	positions, err := ar.client.GetOpenPositions(ctx, ar.safeAddr.Hex())

	fmt.Println("=====", positions, len(positions))
	if err != nil {
		log.Printf("[AutoRedeemer] Failed to get positions: %v", err)
		return
	}

	if len(positions) == 0 {
		return // No positions to check
	}

	// Find resolved positions (curPrice = 0 or 1)
	var redeemable []api.OpenPosition
	for _, pos := range positions {
		curPrice := pos.CurPrice.Float64()
		size := pos.Size.Float64()
		fmt.Println("====0", size, curPrice < 0.001 || curPrice > 0.999)
		// Position is redeemable if:
		// 1. curPrice is exactly 0 or 1 (resolved)
		// 2. We have tokens to redeem
		// 3. Not already submitted
		if size > 0.001 && (curPrice < 0.001 || curPrice > 0.999) {

			fmt.Println("====1", size)
			// Check if already submitted
			key := pos.ConditionID + "-" + pos.Outcome
			ar.submittedRedeemsMu.RLock()
			_, alreadySubmitted := ar.submittedRedeems[key]
			ar.submittedRedeemsMu.RUnlock()

			if !alreadySubmitted {
				redeemable = append(redeemable, pos)
			}
		}
	}

	if len(redeemable) == 0 {
		return // No redeemable positions
	}

	// Limit to max redeems per cycle to stay under rate limit
	if len(redeemable) > maxRedeemsPerCycle {
		log.Printf("[AutoRedeemer] Found %d redeemable positions, processing %d this cycle (rate limit)", len(redeemable), maxRedeemsPerCycle)
		redeemable = redeemable[:maxRedeemsPerCycle]
	} else {
		log.Printf("[AutoRedeemer] Found %d redeemable positions to submit", len(redeemable))
	}

	// Redeem each position via Relayer (gasless!) with delay between calls
	for i, pos := range redeemable {
		// Check if context is cancelled before processing each position
		select {
		case <-ctx.Done():
			log.Printf("[AutoRedeemer] Context cancelled, stopping redemption cycle")
			return
		default:
		}

		// Add delay between calls to avoid rate limits (skip first)
		if i > 0 {
			select {
			case <-ctx.Done():
				log.Printf("[AutoRedeemer] Context cancelled during delay")
				return
			case <-time.After(redeemDelay):
			}
		}

		key := pos.ConditionID + "-" + pos.Outcome

		if err := ar.redeemPosition(ctx, pos); err != nil {
			// Check for quota exceeded error - stop processing if quota is exhausted
			if strings.Contains(err.Error(), "quota exceeded") {
				log.Printf("[AutoRedeemer] ⛔ Quota exceeded! Stopping redemption cycle. Will retry on next cycle.")
				// Don't mark as submitted, so it can be retried later
				break
			}
			log.Printf("[AutoRedeemer] Failed to redeem %s: %v", pos.Title, err)
			continue
		}

		// Mark as submitted
		ar.submittedRedeemsMu.Lock()
		ar.submittedRedeems[key] = time.Now()
		ar.submittedRedeemsMu.Unlock()

		// Track stats
		ar.statsMu.Lock()
		ar.totalRedeemed++
		if pos.CurPrice.Float64() > 0.5 {
			// Won - redeemed at $1.00
			ar.totalUSDC += pos.Size.Float64()
		}
		ar.lastRedeemTime = time.Now()
		ar.statsMu.Unlock()
	}
}

func (ar *AutoRedeemer) redeemPosition(ctx context.Context, pos api.OpenPosition) error {
	log.Printf("[AutoRedeemer] Redeeming via Relayer: %s - %s (condition: %s)",
		pos.Title, pos.Outcome, pos.ConditionID)

	// Convert condition ID to bytes32
	conditionID := common.HexToHash(pos.ConditionID)

	// Determine index set based on outcome (1 = first outcome, 2 = second outcome)
	indexSet := big.NewInt(1)
	if pos.OutcomeIndex == 1 {
		indexSet = big.NewInt(2)
	}

	// Build redeem transaction using api helper
	apiTx, err := api.BuildRedeemTransaction(conditionID, indexSet)
	if err != nil {
		return fmt.Errorf("failed to build redeem tx: %w", err)
	}

	// Convert api.SafeTransaction to relayer/types.SafeTransaction
	relayerTx := types.SafeTransaction{
		To:        apiTx.To.Hex(),
		Operation: types.OperationType(apiTx.Operation),
		Data:      "0x" + hex.EncodeToString(apiTx.Data),
		Value:     apiTx.Value.String(),
	}

	// Execute via Relayer (gasless!)
	// Metadata is limited to 500 characters per Polymarket docs
	metadata := fmt.Sprintf("Redeem: %s - %s", pos.Title, pos.Outcome)
	if len(metadata) > 500 {
		// Truncate if too long
		metadata = metadata[:497] + "..."
	}

	// Create auth option
	authOption := &sdktypes.AuthOption{
		SingerAddress: ar.eoaAddr.Hex(),
		FunderAddress: ar.safeAddr.Hex(),
	}

	resp, err := ar.relayerClient.Execute([]types.SafeTransaction{relayerTx}, metadata, authOption)
	if err != nil {
		// Check if it's a retryable error (server error)
		if strings.Contains(err.Error(), "server error") || strings.Contains(err.Error(), "502") || strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), "504") {
			log.Printf("[AutoRedeemer] ⚠️  Server error during redemption (will retry on next cycle): %v", err)
		} else {
			log.Printf("[AutoRedeemer] ❌ Redemption failed: %v", err)
		}
		return fmt.Errorf("relayer execution failed: %w", err)
	}

	// Extract transaction hash (could be in Hash or TransactionHash field)
	txHash := resp.TransactionHash
	if txHash == "" {
		txHash = resp.Hash
	}

	log.Printf("[AutoRedeemer] ✅ Redemption submitted via Relayer: txID=%s hash=%s state=%s",
		resp.TransactionID, txHash, resp.State)

	return nil
}
