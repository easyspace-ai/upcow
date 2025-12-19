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

	// Handler for trade events
	tradeHandler := polymarketrtds.CreateTradeHandler(func(trade *polymarketrtds.Trade) error {
		fmt.Printf("\n=== Trade Executed ===\n")
		fmt.Printf("Market: %s\n", trade.Market)
		fmt.Printf("Asset ID: %s\n", trade.AssetID)
		fmt.Printf("Side: %s\n", trade.Side)
		fmt.Printf("Outcome: %s\n", trade.Outcome)
		fmt.Printf("Price: %s\n", trade.Price)
		fmt.Printf("Size: %s\n", trade.Size)
		fmt.Printf("Maker: %s\n", trade.MakerAddress)
		fmt.Printf("Taker: %s\n", trade.TakerAddress)
		fmt.Printf("Order Hash: %s\n", trade.OrderHash)
		fmt.Printf("Transaction Hash: %s\n", trade.TransactionHash)
		// Timestamp could be in seconds or milliseconds, detect based on value
		var timestamp time.Time
		if trade.Timestamp > 1e10 {
			// Likely milliseconds (timestamp > year 2001 in seconds)
			timestamp = time.Unix(trade.Timestamp/1000, (trade.Timestamp%1000)*1000000)
		} else {
			// Likely seconds
			timestamp = time.Unix(trade.Timestamp, 0)
		}
		fmt.Printf("Timestamp: %s\n", timestamp.Format(time.RFC3339))
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

	// Register handler for activity topic
	client.RegisterHandler("activity", func(msg *polymarketrtds.Message) error {
		switch msg.Type {
		case "trades":
			return tradeHandler(msg)
		case "orders_matched":
			// orders_matched uses the same Trade structure
			return tradeHandler(msg)
		default:
			fmt.Printf("Unknown activity event type: %s\n", msg.Type)
		}
		return nil
	})

	// Subscribe to activity events
	// Option 1: Subscribe to all trades (no filter)
	fmt.Println("Subscribing to activity events...")
	if err := client.SubscribeToActivity("btc-updown-15m-1765248300", "", "trades", "orders_matched"); err != nil {
		log.Fatalf("Failed to subscribe to activity: %v", err)
	}

	// Option 2: Subscribe to trades for a specific event (uncomment and set eventSlug)
	// eventSlug := "your-event-slug"
	// if err := client.SubscribeToActivity(eventSlug, "", "trades"); err != nil {
	// 	log.Fatalf("Failed to subscribe to activity for event: %v", err)
	// }

	// Option 3: Subscribe to trades for a specific market (uncomment and set marketSlug)
	// marketSlug := "your-market-slug"
	// if err := client.SubscribeToActivity("", marketSlug, "trades"); err != nil {
	// 	log.Fatalf("Failed to subscribe to activity for market: %v", err)
	// }

	fmt.Println("\nMonitoring trading activity. Press Ctrl+C to stop...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
}
