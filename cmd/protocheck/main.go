package main

import (
	"fmt"
	cursorproto "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cursor/proto"
)

func main() {
	ecm := cursorproto.NewMsg("ExecClientMessage")
	
	// Try different field names
	names := []string{
		"mcp_result", "mcpResult", "McpResult", "MCP_RESULT",
		"shell_result", "shellResult",
	}
	
	for _, name := range names {
		fd := ecm.Descriptor().Fields().ByName(name)
		if fd != nil {
			fmt.Printf("Found field %q: number=%d, kind=%s\n", name, fd.Number(), fd.Kind())
		} else {
			fmt.Printf("Field %q NOT FOUND\n", name)
		}
	}
	
	// List all fields
	fmt.Println("\nAll fields in ExecClientMessage:")
	for i := 0; i < ecm.Descriptor().Fields().Len(); i++ {
		f := ecm.Descriptor().Fields().Get(i)
		fmt.Printf("  %d: %q (number=%d)\n", i, f.Name(), f.Number())
	}
}
