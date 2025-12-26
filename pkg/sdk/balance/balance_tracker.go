package balance

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

	"github.com/betbot/gobet/pkg/sdk/api"
)

// BalanceTracker monitors wallet balance and stores history
type BalanceRecord struct {
	Wallet    string    `json:"wallet"`
	Balance   float64   `json:"balance"`
	Timestamp time.Time `json:"timestamp"`
}

type BalanceTracker struct {
	walletAddress string
	interval      time.Duration

	mu            sync.RWMutex
	latestBalance float64
	lastUpdated   time.Time

	stopCh chan struct{}
	wg     sync.WaitGroup
}

// NewBalanceTracker creates a new balance tracker
func NewBalanceTracker(walletAddress string, interval time.Duration) *BalanceTracker {
	return &BalanceTracker{

		walletAddress: walletAddress,
		interval:      interval,
		stopCh:        make(chan struct{}),
	}
}

// Start begins the background balance tracking
func (bt *BalanceTracker) Start() {
	bt.wg.Add(1)
	go bt.trackLoop()
	log.Printf("[BalanceTracker] Started tracking balance for %s every %v", bt.walletAddress, bt.interval)
}

// Stop gracefully stops the balance tracking
func (bt *BalanceTracker) Stop() {
	close(bt.stopCh)
	bt.wg.Wait()
	log.Printf("[BalanceTracker] Stopped")
}

// GetLatestBalance returns the most recently fetched balance
func (bt *BalanceTracker) GetLatestBalance() (float64, time.Time) {
	bt.mu.RLock()
	defer bt.mu.RUnlock()
	return bt.latestBalance, bt.lastUpdated
}

func (bt *BalanceTracker) trackLoop() {
	defer bt.wg.Done()

	// Do an immediate check on start
	bt.checkAndSaveBalance()

	ticker := time.NewTicker(bt.interval)
	defer ticker.Stop()

	for {
		select {
		case <-bt.stopCh:
			return
		case <-ticker.C:
			bt.checkAndSaveBalance()
		}
	}
}

func (bt *BalanceTracker) checkAndSaveBalance() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	balance, err := api.GetOnChainUSDCBalance(ctx, bt.walletAddress)
	if err != nil {
		log.Printf("[BalanceTracker] Error fetching balance: %v", err)
		return
	}
	fmt.Print(balance)

	// Update in-memory cache
	bt.mu.Lock()
	bt.latestBalance = balance
	bt.lastUpdated = time.Now()
	bt.mu.Unlock()

	// Append to local JSON lines file
	record := BalanceRecord{
		Wallet:    bt.walletAddress,
		Balance:   balance,
		Timestamp: bt.lastUpdated,
	}

	data, err := json.Marshal(record)
	if err != nil {
		log.Printf("[BalanceTracker] Error marshaling balance record: %v", err)
		return
	}

	f, err := os.OpenFile("balance_history.jsonl", os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[BalanceTracker] Error opening balance history file: %v", err)
		return
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		log.Printf("[BalanceTracker] Error writing balance record to file: %v", err)
		return
	}

	// Log only significant changes or every minute
	// To avoid spamming logs, we just log silently
	// log.Printf("[BalanceTracker] Balance: $%.2f", balance)
}
