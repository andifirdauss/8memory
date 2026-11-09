// Command 8memory is an MCP memory and journey server for AI coding tools,
// with a TUI for browsing the data and choosing where it is stored.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"8memory/internal/config"
	"8memory/internal/mcpserver"
	"8memory/internal/refs"
	"8memory/internal/store"
	"8memory/internal/tui"
)

const usage = `8memory - shared memory and journey for AI coding tools

Usage:
  8memory serve [--config path]                 Run the MCP server on stdio
  8memory tui   [--config path]                 Browse data and choose the storage backend
  8memory hook  [--config path] [--agent name]  Create a journey node from a prompt-submit hook
  8memory resolve [--config path] <ref>         Print where a file reference is on this machine
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		// stdout belongs to the MCP protocol, so errors only go to stderr.
		fmt.Fprintln(os.Stderr, "8memory:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || !slices.Contains([]string{"serve", "tui", "hook", "resolve"}, args[0]) {
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("expected a command: serve, tui, hook or resolve")
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	configPath := flags.String("config", "", "config file (default ~/.8memory/config.json)")
	agent := flags.String("agent", "claude", "agent name recorded by hook")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *configPath == "" {
		path, err := config.DefaultPath()
		if err != nil {
			return err
		}
		*configPath = path
	}

	switch args[0] {
	case "resolve":
		return resolve(*configPath, flags.Arg(0))
	case "tui":
		return tui.Run(*configPath)
	case "hook":
		// A storage problem must never block the user's prompt.
		if err := runHook(*configPath, *agent, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "8memory hook:", err)
		}
		return nil
	}
	return serve(*configPath)
}

func resolve(configPath, ref string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	path, err := refs.Resolve(ref, cfg.Aliases)
	if err != nil {
		return err
	}
	fmt.Println(path)
	return nil
}

func serve(configPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	openCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	s, err := store.Open(openCtx, cfg)
	if err != nil {
		return err
	}
	defer s.Close()

	return mcpserver.New(s, cfg.Aliases).Run(ctx, &mcp.StdioTransport{})
}
