package main

import (
	"fmt"
	// TODO: cursor proto generation requires protoc; deferred for now
	// cursorproto "github.com/kooshapari/CLIProxyAPI/v7/internal/auth/cursor/proto"
	// "google.golang.org/protobuf/reflect/protoreflect"
)

func main() {
	// TODO: cursor proto generation requires protoc and proto files; deferred for now
	fmt.Println("protocheck utility is temporarily disabled pending cursor proto generation")
	return

	// Original code below requires protoc-generated proto package:
	/*
		msgName := "ExecClientMessage"
		descriptor := cursorproto.Msg(msgName)

		// Try different field names
		names := []string{
			"mcp_result", "mcpResult", "McpResult", "MCP_RESULT",
			"shell_result", "shellResult",
		}

		for _, name := range names {
			fd := descriptor.Fields().ByName(protoreflect.Name(name))
			if fd != nil {
				fmt.Printf("Found field %q: number=%d, kind=%s\n", name, fd.Number(), fd.Kind())
			} else {
				fmt.Printf("Field %q NOT FOUND\n", name)
			}
		}

		// List all fields
		fmt.Println("\nAll fields in ExecClientMessage:")
		for i := 0; i < descriptor.Fields().Len(); i++ {
			f := descriptor.Fields().Get(i)
			fmt.Printf("  %d: %q (number=%d)\n", i, f.Name(), f.Number())
		}
	*/
}
