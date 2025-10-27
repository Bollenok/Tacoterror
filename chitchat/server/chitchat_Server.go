package main

import (
	"fmt"
	"io"
	"net"
	"sync"
	proto "tacoterror/chitchat/grpc"

	"google.golang.org/grpc"
)

// LamportClock holds the state of a Lamport clock.
type LamportClock struct {
	counter int64
	mutex   sync.Mutex
}

func (lc *LamportClock) Increment() {
	lc.mutex.Lock()
	lc.counter++
	lc.mutex.Unlock()
}

func (lc *LamportClock) CompareAndUpdate(receivedTimestamp int64) {
	lc.mutex.Lock()
	if receivedTimestamp > lc.counter {
		lc.counter = receivedTimestamp
	}
	lc.counter++
	lc.mutex.Unlock()
}

func (lc *LamportClock) GetTime() int64 {
	lc.mutex.Lock()
	t := lc.counter
	lc.mutex.Unlock()
	return t
}

// server implements the generated gRPC interface.
type server struct {
	proto.UnimplementedChitChatServer
	clock *LamportClock
	// For a real broadcast you'd track connected streams; this example just echoes back.
}

func newServer() *server {
	return &server{clock: &LamportClock{counter: 0}}
}

// Chat implements the bidi streaming RPC.
// It reads ClientMessage from the stream and replies with ServerMessage(s).
func (s *server) Chat(stream proto.ChitChat_ChatServer) error {
	ctx := stream.Context()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		in, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		switch kind := in.GetKind().(type) {
		case *proto.ClientMessage_Join:
			// acknowledge join
			s.clock.Increment()
			ack := &proto.ServerMessage{
				Kind: &proto.ServerMessage_Ack{
					Ack: &proto.Ack{Info: "joined"},
				},
			}
			if err := stream.Send(ack); err != nil {
				return err
			}
		case *proto.ClientMessage_Chat:
			chat := kind.Chat
			// update lamport with received timestamp then increment for receive event
			s.clock.CompareAndUpdate(chat.LogicalTime)
			// build broadcast (here sent only back on same stream)
			b := &proto.ServerMessage{
				Kind: &proto.ServerMessage_Broadcast{
					Broadcast: &proto.Broadcast{
						Type:        proto.BroadcastType_BROADCAST_MESSAGE,
						Sender:      "unknown", // fill from join state if you track it
						Text:        chat.Text,
						LogicalTime: s.clock.GetTime(),
					},
				},
			}
			if err := stream.Send(b); err != nil {
				return err
			}
		case *proto.ClientMessage_Leave:
			// acknowledge leave
			s.clock.Increment()
			ack := &proto.ServerMessage{
				Kind: &proto.ServerMessage_Ack{
					Ack: &proto.Ack{Info: "left"},
				},
			}
			if err := stream.Send(ack); err != nil {
				return err
			}
		default:
			// unknown oneof
			s.clock.Increment()
			_ = stream.Send(&proto.ServerMessage{
				Kind: &proto.ServerMessage_Error{
					Error: &proto.Error{Code: 400, Message: "unknown message"},
				},
			})
		}
	}
}

func main() {
	lis, err := net.Listen("tcp", ":50051")
	if err != nil {
		fmt.Println("failed to listen:", err)
		return
	}
	grpcServer := grpc.NewServer()
	proto.RegisterChitChatServer(grpcServer, newServer())

	fmt.Println("gRPC server listening on :50051")
	if err := grpcServer.Serve(lis); err != nil {
		fmt.Println("gRPC server failed:", err)
	}
}
