package main

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"github.com/habibmuhammad/deploymate/internal/mcp"
)

// version is stamped into the MCP server's identity.
const version = "dev"

// runMCP serves the Model Context Protocol on stdin/stdout. It talks to a
// running DeployMate over its /api/v1 with a token, so it needs only two
// settings — never the database, the key file or Docker:
//
//	DEPLOYMATE_URL    the dashboard, default http://127.0.0.1:8090
//	DEPLOYMATE_TOKEN  an API token from /settings/tokens
//
// stdout carries the protocol and nothing else; diagnostics go to stderr.
func runMCP() error {
	token := os.Getenv("DEPLOYMATE_TOKEN")
	if token == "" {
		return errors.New("DEPLOYMATE_TOKEN is not set — create an API token at /settings/tokens and pass it in the environment")
	}
	url := os.Getenv("DEPLOYMATE_URL")
	if url == "" {
		url = "http://127.0.0.1:8090"
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	return mcp.New(url, token, version, os.Stderr).Serve(ctx, os.Stdin, os.Stdout)
}
