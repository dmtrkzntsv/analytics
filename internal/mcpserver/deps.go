// Package mcpserver pins Phase B dependencies; real code lands in the next task.
package mcpserver

import (
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/modelcontextprotocol/go-sdk/mcp"
)
