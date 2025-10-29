# Read before running
Before running the program, make sure you have:
- Go 1.21+ installed
- Protocol Buffers Compiler (protoc)
- gRPC tools for Go installed

`go install google.golang.org/protobuf/cmd/protoc-gen-go@latest`

`go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest`
  
and make sure that `$GOPATH/bin` is in your system PATH.

# Running the Server
1. Open the terminal in the project root
2. Navigate to `cd chitchat/server` and run:

`go run chitchat_Server.go`

# Running clients
1. Open a new terminal in the project root for each client
2. Navigate to `cd chitchat/client` and run:

`go run chitchat_Client.go`

4. Enter a username
5. To shut a client down, press Ctrl-c

Client logs can be accessed in `cd chitchat/client/client_logs`
