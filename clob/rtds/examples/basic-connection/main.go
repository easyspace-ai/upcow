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

	// Register a wildcard handler to see all messages
	client.RegisterHandler("*", func(msg *polymarketrtds.Message) error {
		fmt.Printf("[%s] Topic: %s, Type: %s, Timestamp: %d\n",
			time.Now().Format(time.RFC3339),
			msg.Topic,
			msg.Type,
			msg.Timestamp)
		return nil
	})

	// Connect to RTDS
	fmt.Println("Connecting to Polymarket RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("Connected successfully!")

	// Subscribe to crypto prices (Binance)
	fmt.Println("Subscribing to Binance crypto prices...")
	if err := client.SubscribeToCryptoPrices("binance", "btcusdt", "ethusdt"); err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}

	// Subscribe to comments
	fmt.Println("Subscribing to comments...")
	if err := client.SubscribeToComments(nil, "Event", "*"); err != nil {
		log.Fatalf("Failed to subscribe to comments: %v", err)
	}

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Keep running until interrupted
	<-sigChan
	fmt.Println("\nShutting down...")
}
