package main

import (
	"context"
	"sync"

	"log"
	"path/to/your/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// LamportClock holds the state of a Lamport clock.
type LamportClock struct {
	counter int
	mutex   sync.Mutex
}

func main() {
	lc := &LamportClock{counter: 0}

	conn, err := grpc.NewClient("localhost:5050", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatalf("Not working")
	}

	client := proto.ne(conn)

	students, err := client.GetStudents(context.Background(), &proto.Empty{})
	if err != nil {
		log.Fatalf("Not working")
	}

	for _, student := range students.Students {
		println(" - " + student)

	}
}

// Increment the clock by 1 (local event).
func (lc *LamportClock) Increment() {
	lc.mutex.Lock()
	lc.counter++
	lc.mutex.Unlock()
}

// CompareAndUpdate compares the current clock value with the received one, and updates the current clock with the maximum of the two, incremented by 1 (message receive event).
func (lc *LamportClock) CompareAndUpdate(receivedTimestamp int) {
	lc.mutex.Lock()
	if receivedTimestamp > lc.counter {
		lc.counter = receivedTimestamp
	}
	lc.counter++
	lc.mutex.Unlock()
}

// GetTime gets the current time from the Lamport clock.
func (lc *LamportClock) GetTime() int {
	lc.mutex.Lock()
	currentTime := lc.counter
	lc.mutex.Unlock()
	return currentTime
}
