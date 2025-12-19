package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	polymarketrtds "github.com/betbot/gobet/clob/rtds"
)

func main() {
	// Create a new RTDS client
	client := polymarketrtds.NewClient()

	// Handler for aggregated orderbook updates
	orderbookHandler := polymarketrtds.CreateAggOrderbookHandler(func(orderbook *polymarketrtds.AggOrderbook) error {
		fmt.Printf("\n=== Orderbook Update ===\n")
		fmt.Printf("Market: %s\n", orderbook.Market)
		fmt.Printf("Asset ID: %s\n", orderbook.AssetID)
		fmt.Printf("Hash: %s\n", orderbook.Hash)
		fmt.Printf("Tick Size: %s\n", orderbook.TickSize)
		fmt.Printf("Min Order Size: %s\n", orderbook.MinOrderSize)
		fmt.Printf("Neg Risk: %v\n", orderbook.NegRisk)
		fmt.Printf("Timestamp: %s\n", orderbook.Timestamp)

		fmt.Printf("\nTop 5 Bids:\n")
		for i, bid := range orderbook.Bids {
			if i >= 5 {
				break
			}
			fmt.Printf("  Price: %s, Size: %s\n", bid.Price, bid.Size)
		}

		fmt.Printf("\nTop 5 Asks:\n")
		for i, ask := range orderbook.Asks {
			if i >= 5 {
				break
			}
			fmt.Printf("  Price: %s, Size: %s\n", ask.Price, ask.Size)
		}
		fmt.Printf("======================\n\n")
		return nil
	})

	// Handler for last trade price updates
	lastTradePriceHandler := polymarketrtds.CreateLastTradePriceHandler(func(price *polymarketrtds.LastTradePrice) error {
		fmt.Printf("\n=== Last Trade Price ===\n")
		fmt.Printf("Market: %s\n", price.Market)
		fmt.Printf("Asset ID: %s\n", price.AssetID)
		fmt.Printf("Price: %s\n", price.Price)
		fmt.Printf("Timestamp: %s\n", price.Timestamp)
		fmt.Printf("=======================\n\n")
		return nil
	})

	// Handler for price changes
	priceChangesHandler := polymarketrtds.CreatePriceChangesHandler(func(changes *polymarketrtds.PriceChanges) error {
		fmt.Printf("\n=== Price Changes ===\n")
		for market, change := range changes.Markets {
			fmt.Printf("Market: %s, Asset ID: %s, Price: %s, Timestamp: %s\n",
				market, change.AssetID, change.Price, change.Timestamp)
		}
		fmt.Printf("====================\n\n")
		return nil
	})

	// Connect to RTDS
	fmt.Println("Connecting to Polymarket RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("Connected successfully!")

	// Register handlers
	client.RegisterHandler("clob_market", func(msg *polymarketrtds.Message) error {
		switch msg.Type {
		case "agg_orderbook":
			return orderbookHandler(msg)
		case "last_trade_price":
			return lastTradePriceHandler(msg)
		case "price_change":
			return priceChangesHandler(msg)
		default:
			fmt.Printf("Unknown CLOB market event type: %s\n", msg.Type)
		}
		return nil
	})

	// Subscribe to CLOB market data
	// Replace with actual market IDs from Polymarket
	// You can get market IDs from the Gamma API or from market URLs
	marketIDs := []string{
		// Example market IDs - replace with real ones
		// "0x1234...",
		// "0x5678...",
	}

	if len(marketIDs) == 0 {
		fmt.Println("WARNING: No market IDs provided. Please add market IDs to subscribe to.")
		fmt.Println("You can get market IDs from the Gamma API or from market URLs.")
		fmt.Println("Example: marketIDs := []string{\"0x1234...\", \"0x5678...\"}")
		os.Exit(1)
	}

	fmt.Printf("Subscribing to CLOB market data for %d markets...\n", len(marketIDs))
	if err := client.SubscribeToClobMarket(marketIDs, "agg_orderbook", "last_trade_price", "price_change"); err != nil {
		log.Fatalf("Failed to subscribe to CLOB market data: %v", err)
	}

	fmt.Println("\nMonitoring CLOB market data. Press Ctrl+C to stop...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
}
