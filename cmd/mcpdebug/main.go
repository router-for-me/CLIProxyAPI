package main

import (
	"encoding/hex"
	"fmt"
	"os"

	cursorproto "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cursor/proto"
)

func main() {
	// Encode MCP result with empty execId
	resultBytes := cursorproto.EncodeExecMcpResult(1, "", `{"test": "data"}`, false)
	fmt.Printf("Result protobuf hex: %s\n", hex.EncodeToString(resultBytes))
	fmt.Printf("Result length: %d bytes\n", len(resultBytes))

	// Write to file for analysis
	os.WriteFile("mcp_result.bin", resultBytes, 0644)
	fmt.Println("Wrote mcp_result.bin")
}
