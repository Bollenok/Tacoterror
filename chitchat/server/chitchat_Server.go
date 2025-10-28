package main

import (
	"fmt"
	"io"
	"log"
	"net"
	"sync"

	proto "tacoterror/chitchat/grpc"

	"google.golang.org/grpc"
)

const (
	maxLen = 128
)

// -------------------------- LAMPORT CLOCK ------------------------------------
// LamportClock holds the state of a Lamport clock
type LamportClock struct {
	counter int64
	mutex   sync.Mutex
}

// Increment advances the clock for a local event and returns the new time
func (lc *LamportClock) Increment() int64 {
	lc.mutex.Lock()
	lc.counter++
	t := lc.counter
	lc.mutex.Unlock()
	return t
}

func (lc *LamportClock) CompareAndUpdate(receivedTimestamp int64) int64 {
	lc.mutex.Lock()
	if receivedTimestamp > lc.counter {
		lc.counter = receivedTimestamp
	}
	lc.counter++
	t := lc.counter
	lc.mutex.Unlock()
	return t
}

func (lc *LamportClock) GetTime() int64 {
	lc.mutex.Lock()
	t := lc.counter
	lc.mutex.Unlock()
	return t
}

// ------------------------------ SERVER TYPES ------------------------------------------

type clientConn struct {
	name   string
	stream proto.ChitChat_ChatServer
	sendQ  chan *proto.ServerMessage
}

// centralize ordering and broadcasting
type eventType int

const (
	evJoin  eventType = iota // join
	evChat                   // chat message
	evLeave                  // leave
)

type event struct {
	typ  eventType
	name string
	text string
	lt   int64 // client’s lamport time
	src  *clientConn
}

type server struct {
	proto.UnimplementedChitChatServer

	mu      sync.RWMutex
	clients map[string]*clientConn // active clients registry

	clock  LamportClock // shared lamport clock for the server
	events chan event   // centralized event channel
}

// ------------------ SERVER CONSTRUCTOR ---------------------------

func newServer() *server {
	s := &server{
		clients: make(map[string]*clientConn),
		events:  make(chan event, 256),
	}
	go s.dispatcher()
	return s
}

func (s *server) dispatcher() {
	for ev := range s.events {
		s.clock.CompareAndUpdate(ev.lt)
		bLamport := s.clock.Increment()

		var bType proto.BroadcastType
		var text string
		switch ev.typ {
		case evJoin: // JOIN
			bType = proto.BroadcastType_BROADCAST_JOIN
			text = fmt.Sprintf("Participant %s joined Chit Chat at logical time %d", ev.name, bLamport)
			log.Printf("component=Server event=CLIENT_CONNECTED name=%s lamport=%d", ev.name, bLamport) // log
		case evLeave: // LEAVE
			bType = proto.BroadcastType_BROADCAST_LEAVE
			text = fmt.Sprintf("Participant %s left Chit Chat at logical time %d", ev.name, bLamport)
			log.Printf("component=Server event=CLIENT_DISCONNECTED name=%s lamport=%d", ev.name, bLamport) // log
		case evChat: // CHAT
			bType = proto.BroadcastType_BROADCAST_MESSAGE
			text = ev.text
			log.Printf("component=Server event=BROADCAST_PREPARED from=%s lamport=%d", ev.name, bLamport) // log
		}

		msg := &proto.ServerMessage{ // broadcast includes lamport timestamp
			Kind: &proto.ServerMessage_Broadcast{
				Broadcast: &proto.Broadcast{
					Type:        bType,
					Sender:      ev.name,
					Text:        text,
					LogicalTime: bLamport,
				},
			},
		}

		// using per-client buffered queues.
		s.mu.RLock()
		for name, c := range s.clients {
			select {
			case c.sendQ <- msg:
			default:
				log.Printf("component=Server event=BROADCAST_DROP to=%s reason=sendQ_full", name) // log
			}
		}
		s.mu.RUnlock()

		log.Printf("component=Server event=BROADCAST_SENT type=%v from=%s lamport=%d", bType, ev.name, bLamport) // log
	}
}

// -------------- HELPER FUNCTIONS --------------------------------
func (s *server) register(name string, c *clientConn) error {
	if name == "" {
		return fmt.Errorf("empty name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.clients[name]; exists {
		return fmt.Errorf("name '%s' already in use", name)
	}
	c.name = name
	s.clients[name] = c
	log.Printf("component=Server event=REGISTER name=%s", name)
	return nil
}

func (s *server) unregister(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.clients[name]; ok {
		delete(s.clients, name)
		close(c.sendQ) // stop sender goroutine cleanly
	}
}

func sendError(stream proto.ChitChat_ChatServer, code int32, msg string) {
	_ = stream.Send(&proto.ServerMessage{
		Kind: &proto.ServerMessage_Error{
			Error: &proto.Error{Code: code, Message: msg},
		},
	})
}

// Chat implements the bidi streaming RPC.
// It reads ClientMessage from the stream and replies with ServerMessage(s).
func (s *server) Chat(stream proto.ChitChat_ChatServer) error {
	ctx := stream.Context()

	var cl *clientConn
	defer func() {
		if cl != nil {
			s.unregister(cl.name) // remove from map, close sendQ, etc.
		}
	}()

	c := &clientConn{
		stream: stream,
		sendQ:  make(chan *proto.ServerMessage, 64),
	}

	// Sender goroutine: drains sendQ -> stream.Send (prevents blocking the dispatcher)
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-c.sendQ:
				if !ok {
					return
				}
				if err := stream.Send(msg); err != nil {
					log.Printf("component=Server event=STREAM_SEND_ERROR name=%s err=%v", c.name, err)
					return
				}
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			if c.name != "" {
				name := c.name
				s.unregister(name)
				lt := s.clock.GetTime()
				s.events <- event{typ: evLeave, name: name, lt: lt, src: c}
			}
			return ctx.Err()
		default:
		}

		in, err := stream.Recv()
		if err == io.EOF {
			fmt.Println("Client disconnected (EOF)")
			if c.name != "" {
				name := c.name
				s.unregister(name)
				lt := s.clock.GetTime()
				s.events <- event{typ: evLeave, name: name, lt: lt, src: c}
			}
			return nil
		}
		if err != nil {
			fmt.Printf("Error receiving message: %v\n", err)
			if c.name != "" {
				name := c.name
				s.unregister(name)
				lt := s.clock.GetTime()
				s.events <- event{typ: evLeave, name: name, lt: lt, src: c}
			}
			return err
		}

		// ------------------ JOIN, MESSAGE, LEAVE ------------------------
		switch kind := in.GetKind().(type) {

		case *proto.ClientMessage_Join:
			s.clock.CompareAndUpdate(kind.Join.GetLogicalTime())
			name := kind.Join.GetName()
			fmt.Printf("[%d] Client joined: %s\n", s.clock.GetTime(), name)

			// validation + error sending
			if name == "" {
				sendError(stream, 400, "empty name")
				continue
			}

			if err := s.register(name, c); err != nil {
				sendError(stream, 409, err.Error())
				continue
			}

			if err := s.register(name, c); err != nil {
				fmt.Print(c, 409, err.Error())
				// keep stream alive so client can retry with a new name
				continue
			}

			ack := &proto.ServerMessage{
				Kind: &proto.ServerMessage_Ack{
					Ack: &proto.Ack{Info: fmt.Sprintf("%s joined", name)},
				},
			}

			if err := stream.Send(ack); err != nil {
				fmt.Printf("Failed to send ack: %v\n", err)
				return err
			}

			// Tell everyone (via dispatcher) that someone joined
			s.events <- event{typ: evJoin, name: name, lt: kind.Join.GetLogicalTime(), src: c}

		case *proto.ClientMessage_Chat:
			if c.name == "" {
				sendError(stream, 401, "must Join first")
				continue
			}

			chat := kind.Chat
			fmt.Printf("💬 [%d] Chat from client: \"%s\"\n", s.clock.GetTime(), chat.Text)

			// message must be >= 128 characters
			if len([]rune(chat.GetText())) >= maxLen {
				sendError(stream, 400, fmt.Sprintf("message too long (>%d)", maxLen))
				continue
			}

			s.clock.CompareAndUpdate(chat.GetLogicalTime())
			s.events <- event{typ: evChat, name: c.name, text: chat.GetText(), lt: chat.GetLogicalTime(), src: c}

		case *proto.ClientMessage_Leave:
			if c.name == "" {
				sendError(stream, 401, "must Join first")
				continue
			}

			s.clock.CompareAndUpdate(kind.Leave.GetLogicalTime())
			name := c.name
			fmt.Printf("[%d] Client left: %s\n", s.clock.GetTime(), name)
			s.unregister(name)
			s.events <- event{typ: evLeave, name: name, lt: kind.Leave.GetLogicalTime(), src: c}

			ack := &proto.ServerMessage{
				Kind: &proto.ServerMessage_Ack{
					Ack: &proto.Ack{Info: fmt.Sprintf("%s left", name)},
				},
			}
			if err := stream.Send(ack); err != nil {
				fmt.Printf("Failed to send ack: %v\n", err)
				return err
			}

			return nil

		default:
			s.clock.Increment()
			fmt.Printf("[%d] Unknown message type\n", s.clock.GetTime())
			sendError(stream, 400, "unknown message") // send error through helper
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
