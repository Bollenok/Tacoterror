package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
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
	logDir := "chitchat/client/client_logs"
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, username+".log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer f.Close()
	logger := log.New(f, "", log.LstdFlags)

	/*logDir := "chitchat/client/client_logs"
	if err := os.MkdirAll(logDir, os.ModePerm); err != nil { // make sure it exists
		log.Fatalf("Failed to create log directory: %v", err)
	}
	logFileName := fmt.Sprintf("%s/client_%s.log", logDir, username)
	logFile, err := os.Create(logFileName)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer logFile.Close()
	logger := log.New(logFile, "", log.LstdFlags)*/

	// Establish gRPC connection
	conn, err := grpc.NewClient("localhost:50051", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}
	defer conn.Close()

	client := proto.NewChitChatClient(conn)

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	stream, err := client.Chat(streamCtx)
	if err != nil {
		log.Fatalf("Failed to open chat stream: %v", err)
	}

	lc := &LamportClock{counter: 0}

	// Notify the service of join event
	ltJoin := lc.Increment()
	join := &proto.ClientMessage{
		Kind: &proto.ClientMessage_Join{
			Join: &proto.Join{Name: username, LogicalTime: ltJoin},
		},
	}
	if err := stream.Send(join); err != nil {
		log.Fatalf("Failed to send join message: %v", err)
	}
	fmt.Printf("🕒 [%d] Joined as %s\n", lc.Increment(), username)

	// Concurrent listener for server broadcasts
	errCh := make(chan error, 1)

	go func() {
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				fmt.Println("Server closed connection.")
				errCh <- nil
				return
			}
			if err != nil {
				log.Printf("Receive error: %v\n", err)
				errCh <- err
				return
			}

			if broadcastMsg, ok := msg.Kind.(*proto.ServerMessage_Broadcast); ok {
				b := broadcastMsg.Broadcast
				newTime := lc.CompareAndUpdate(b.LogicalTime)

				sender := b.Sender
				if sender == username {
					sender = "You"
				}

				display := fmt.Sprintf("[Lamport %d] %s: %s", newTime, sender, b.Text)
				fmt.Println(display)
				logger.Println(display)
			}
		}
	}()

	// Capture interrupt signal for graceful termination
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	// Interactive message publishing loop
	fmt.Println("You can type messages now (≤128 chars). Ctrl-C to leave.")
	for {
		fmt.Print("> ")
		text, _ := reader.ReadString('\n')
		text = strings.TrimSpace(text)

		select {
		case <-stop:
			ltLeave := lc.Increment()
			leave := &proto.ClientMessage{
				Kind: &proto.ClientMessage_Leave{
					Leave: &proto.Leave{Name: username, LogicalTime: ltLeave},
				},
			}
			_ = stream.Send(leave)
			fmt.Println("Sent leave message. Goodbye!")
			time.Sleep(500 * time.Millisecond)
			_ = stream.CloseSend()
			return
		default:
		}

		if len(text) == 0 {
			continue
		}
		if len([]rune(text)) > 128 {
			fmt.Println("Message too long (max 128 chars).")
			continue
		}

		ltMsg := lc.Increment()
		msg := &proto.ClientMessage{
			Kind: &proto.ClientMessage_Chat{
				Chat: &proto.ChatMessage{
					Sender: username, Text: text, LogicalTime: ltMsg,
				},
			},
		}
		if err := stream.Send(msg); err != nil {
			log.Printf("send chat failed: %v", err)
			return
		}
	}
}
