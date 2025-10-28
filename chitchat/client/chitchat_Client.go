package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	proto "tacoterror/chitchat/grpc"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ============================================================================
// LamportClock
// A thread-safe implementation of a Lamport logical clock used to order events
// in the distributed chat system without relying on synchronized physical time.
// ============================================================================
type LamportClock struct {
	counter int64
	mutex   sync.Mutex
}

func (lc *LamportClock) Increment() int64 {
	lc.mutex.Lock()
	defer lc.mutex.Unlock()
	lc.counter++
	return lc.counter
}

func (lc *LamportClock) CompareAndUpdate(received int64) int64 {
	lc.mutex.Lock()
	defer lc.mutex.Unlock()
	if received > lc.counter {
		lc.counter = received
	}
	lc.counter++
	return lc.counter
}

func (lc *LamportClock) GetTime() int64 {
	lc.mutex.Lock()
	defer lc.mutex.Unlock()
	return lc.counter
}

// ============================================================================
// main
// Initializes the ChitChat client, connects to the gRPC service, handles user
// input, maintains Lamport time, and logs/display messages from the server.
// ============================================================================
func main() {
	reader := bufio.NewReader(os.Stdin)
	fmt.Print("Enter your username: ")
	username, _ := reader.ReadString('\n')
	username = strings.TrimSpace(username)
	if username == "" {
		fmt.Println("Username cannot be empty.")
		return
	}

	// Create per-user log file
	logFileName := fmt.Sprintf("client_%s.log", username)
	logFile, err := os.Create(logFileName)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)

	lc := &LamportClock{counter: 0}

	// Establish gRPC connection
	conn, err := grpc.NewClient("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	client := proto.NewChitChatClient(conn)
	stream, err := client.Chat(context.Background())
	if err != nil {
		log.Fatalf("Failed to open chat stream: %v", err)
	}

	// Notify the service of join event
	join := &proto.ClientMessage{
		Kind: &proto.ClientMessage_Join{
			Join: &proto.Join{Name: username},
		},
	}
	if err := stream.Send(join); err != nil {
		log.Fatalf("Failed to send join message: %v", err)
	}
	fmt.Printf("🕒 [%d] Joined as %s\n", lc.Increment(), username)

	// Concurrent listener for server broadcasts
	go func() {
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				fmt.Println("Server closed connection.")
				return
			}
			if err != nil {
				log.Printf("Receive error: %v\n", err)
				return
			}

			if broadcastMsg, ok := msg.Kind.(*proto.ServerMessage_Broadcast); ok {
				b := broadcastMsg.Broadcast
				newTime := lc.CompareAndUpdate(b.LogicalTime)
				display := fmt.Sprintf("[Lamport %d] %s: %s", newTime, b.Sender, b.Text)
				fmt.Println(display)
				logger.Println(display)
			}
		}
	}()

	// Capture interrupt signal for graceful termination
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Interactive message publishing loop
	for {
		fmt.Print("> ")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)

		select {
		case <-stop:
			leave := &proto.ClientMessage{
				Kind: &proto.ClientMessage_Leave{
					Leave: &proto.Leave{Name: username},
				},
			}
			_ = stream.Send(leave)
			fmt.Println("Sent leave message. Goodbye!")
			time.Sleep(500 * time.Millisecond)
			return
		default:
		}

		if len(text) == 0 {
			continue
		}
		if len(text) > 128 {
			fmt.Println("Message too long (max 128 chars).")
			continue
		}

		lc.Increment()
		fmt.Printf("[%d] You: %s\n", lc.GetTime(), text)
		logger.Printf("[Lamport %d] You: %s\n", lc.GetTime(), text)
	}
}
