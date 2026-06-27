package main

import (
	"fmt"
	// TODO: cursor proto generation requires protoc; deferred for now
	// "encoding/hex"
	// "os"
	// cursorproto "github.com/kooshapari/CLIProxyAPI/v7/internal/auth/cursor/proto"
)

func main() {
	// TODO: cursor proto generation requires protoc and proto files; deferred for now
	fmt.Println("mcpdebug utility is temporarily disabled pending cursor proto generation")
	return

	// Original code below requires protoc-generated proto package:
	/*
		resultBytes := cursorproto.EncodeExecMcpResult(1, "", `{"test": "data"}`, false)
		...
	*/
}
