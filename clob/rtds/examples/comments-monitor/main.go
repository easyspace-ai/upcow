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

	// Handler for comment created events
	commentHandler := polymarketrtds.CreateCommentHandler(func(comment *polymarketrtds.Comment) error {
		fmt.Printf("\n=== New Comment ===\n")
		fmt.Printf("ID: %s\n", comment.ID)
		fmt.Printf("User: %s (@%s)\n", comment.Profile.Name, comment.Profile.Pseudonym)
		fmt.Printf("Body: %s\n", comment.Body)
		fmt.Printf("Parent Entity: %s (ID: %d)\n", comment.ParentEntityType, comment.ParentEntityID)
		if comment.ParentCommentID != nil {
			fmt.Printf("Reply to: %s\n", *comment.ParentCommentID)
		}
		fmt.Printf("Reactions: %d\n", comment.ReactionCount)
		fmt.Printf("Created: %s\n", comment.CreatedAt.Time().Format(time.RFC3339))
		fmt.Printf("==================\n\n")
		return nil
	})

	// Handler for reaction events
	reactionHandler := polymarketrtds.CreateReactionHandler(func(reaction *polymarketrtds.Reaction) error {
		fmt.Printf("\n=== New Reaction ===\n")
		fmt.Printf("Type: %s (%s)\n", reaction.ReactionType, reaction.Icon)
		fmt.Printf("Comment ID: %d\n", reaction.CommentID)
		fmt.Printf("User: %s\n", reaction.UserAddress)
		fmt.Printf("Created: %s\n", reaction.CreatedAt.Time().Format(time.RFC3339))
		fmt.Printf("==================\n\n")
		return nil
	})

	// Connect to RTDS
	fmt.Println("Connecting to Polymarket RTDS...")
	if err := client.Connect(); err != nil {
		log.Fatalf("Failed to connect: %v", err)
	}
	defer client.Disconnect()

	fmt.Println("Connected successfully!")

	// Subscribe to all comment events
	fmt.Println("Subscribing to comment events...")
	client.RegisterHandler("comments", func(msg *polymarketrtds.Message) error {
		switch msg.Type {
		case "comment_created", "comment_removed":
			return commentHandler(msg)
		case "reaction_created", "reaction_removed":
			return reactionHandler(msg)
		default:
			fmt.Printf("Unknown comment event type: %s\n", msg.Type)
		}
		return nil
	})

	// Subscribe to comments for all events (no filter)
	if err := client.SubscribeToComments(nil, "Event", "comment_created", "reaction_created"); err != nil {
		log.Fatalf("Failed to subscribe to comments: %v", err)
	}

	// Example: Subscribe to comments for a specific event (uncomment and set eventID)
	// eventID := 100
	// if err := client.SubscribeToComments(&eventID, "Event", "comment_created", "reaction_created"); err != nil {
	// 	log.Fatalf("Failed to subscribe to comments for event: %v", err)
	// }

	fmt.Println("\nMonitoring comments. Press Ctrl+C to stop...")

	// Wait for interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nShutting down...")
}
