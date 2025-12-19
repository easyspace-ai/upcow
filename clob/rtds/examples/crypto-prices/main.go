package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	polymarketrtds "github.com/betbot/gobet/clob/rtds"
)

func main() {
	// Create a new RTDS client
	client := polymarketrtds.NewClient()

	// Handler for Binance crypto prices
	binanceHandler := polymarketrtds.CreateCryptoPriceHandler(func(price *polymarketrtds.CryptoPrice) error {
		// Timestamp is in milliseconds, convert to time.Time
		timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)
		fmt.Printf("[Binance] %s: $%.2f (time: %s)\n",
			price.Symbol,
			price.Value,
			timestamp.Format(time.RFC3339))
		return nil
	})

	// Handler for Chainlink crypto prices
	chainlinkHandler := polymarketrtds.CreateCryptoPriceHandler(func(price *polymarketrtds.CryptoPrice) error {
		// Timestamp is in milliseconds, convert to time.Time
		timestamp := time.Unix(price.Timestamp/1000, (price.Timestamp%1000)*1000000)
		fmt.Printf("[Chainlink] %s: $%.2f (time: %s)\n",
			price.Symbol,
			price.Value,
			timestamp.Format(time.RFC3339))
		return nil
	})

	// Connect to RTDS
	fmt.Println("Connecting to Polymarket RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("Connected successfully!")

	// Subscribe to Binance prices
	fmt.Println("Subscribing to Binance crypto prices (BTC, ETH, SOL)...")
	client.RegisterHandler("crypto_prices", binanceHandler)
	if err := client.SubscribeToCryptoPrices("binance", "btcusdt", "ethusdt", "solusdt"); err != nil {
		log.Fatalf("Failed to subscribe to Binance prices: %v", err)
	}

	// Wait a bit before subscribing to Chainlink
	time.Sleep(1 * time.Second)

	// Subscribe to Chainlink prices
	fmt.Println("Subscribing to Chainlink crypto prices (BTC/USD, ETH/USD)...")
	client.RegisterHandler("crypto_prices_chainlink", chainlinkHandler)
	if err := client.SubscribeToCryptoPrices("chainlink", "btc/usd", "eth/usd"); err != nil {
		log.Fatalf("Failed to subscribe to Chainlink prices: %v", err)
	}

	fmt.Println("\nMonitoring crypto prices. Press Ctrl+C to stop...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
}
