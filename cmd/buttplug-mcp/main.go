// Copyright (c) 2025 Neomantra BV

package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"sync"

	"github.com/ConAcademy/buttplug-mcp/internal/bp"
	"github.com/ConAcademy/buttplug-mcp/internal/mcp"
	"github.com/spf13/pflag"
)

///////////////////////////////////////////////////////////////////////////////

const (
	mcpServerVersion = "0.0.1"

	defaultSSEHostPort = ":8889"
	defaultLogDest     = "butplug-mcp.log"
)

type Config struct {
	LogJSON bool // Log in JSON format instead of text
	Verbose bool // Verbose logging

	MCPConfig mcp.Config // MCP config
	BPConfig  bp.Config  // Buttplug config
}

///////////////////////////////////////////////////////////////////////////////

func main() {
	var config Config
	var logFilename string

	pflag.StringVarP(&logFilename, "log-file", "l", "", "Log file destination (or MCP_LOG_FILE envvar). Default is stderr")
	pflag.BoolVarP(&config.LogJSON, "log-json", "j", false, "Log in JSON (default is plaintext)")
	pflag.StringVarP(&config.MCPConfig.SSEHostPort, "sse-host", "", "", "host:port to listen to SSE connections")
	pflag.BoolVarP(&config.MCPConfig.UseSSE, "sse", "", false, "Use SSE Transport (default is stdio transport)")
	pflag.StringVarP(&config.MCPConfig.ToolDescriptionsFile, "tool-descriptions", "", "", "Path to YAML file with tool descriptions to override defaults (run `tool-descriptions' to see current descriptions in YAML)")
	pflag.StringVarP(&config.BPConfig.WebsocketHost, "ws-host", "", "localhost", "host to connect to the Buttplug Websocket server")
	pflag.IntVarP(&config.BPConfig.WebsocketPort, "ws-port", "", 12345, "port to connect to the Buttplug Websocket server")
	pflag.BoolVarP(&config.Verbose, "verbose", "v", false, "Verbose logging")
	pflag.Usage = func() {
		o := pflag.CommandLine.Output()
		fmt.Fprintf(o, "usage: %s [|start|tool-descriptions] [flags]\n", os.Args[0])
		fmt.Fprintf(o, "flags:\n")
		pflag.PrintDefaults()
	}
	pflag.Parse()

	if config.MCPConfig.SSEHostPort == "" {
		config.MCPConfig.SSEHostPort = defaultSSEHostPort
	}

	config.MCPConfig.Name = "buttplug-mcp"
	config.MCPConfig.Version = mcpServerVersion

	// Set up logging
	logWriter := os.Stderr // default is stderr
	if logFilename == "" { // prefer CLI option
		logFilename = os.Getenv("MCP_LOG_FILE")
	}
	if logFilename != "" {
		logFile, err := os.OpenFile(logFilename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatal("failed to open log file:", err)
		}
		logWriter = logFile
		defer logFile.Close()
	}

	var logLevel = slog.LevelInfo
	if config.Verbose {
		logLevel = slog.LevelDebug
	}

	var logger *slog.Logger
	if config.LogJSON {
		logger = slog.New(slog.NewJSONHandler(logWriter, &slog.HandlerOptions{Level: logLevel}))
	} else {
		logger = slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: logLevel}))
	}

	bpm, err := bp.NewManager(config.BPConfig, logger)
	if err != nil {
		log.Fatal("failed to create Buttplug manager:", err)
	}

	mcps, err := mcp.New(config.MCPConfig, bpm, logger)
	if err != nil {
		log.Fatal("failed to create MCP server:", err)
	}

	switch pflag.Arg(0) {
	case "tool-descriptions":
		yaml, err := mcp.FormatToolDescriptionsAsYAML(mcps.ToolDescriptions())
		if err != nil {
			log.Fatal("failed to format tool descriptions as YAML:", err)
		}
		fmt.Println(string(yaml))
		return

	case "start", "":
		// continue to starting the server

	default:
		log.Fatalf("unknown command: %s", pflag.Arg(0))
	}

	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	wg.Go(func() {
		if err := bpm.Run(ctx); err != nil {
			logger.Error(
				"buttplug manager error, program is useless now",
				"error", err.Error())
		}
	})

	if err := mcps.Start(); err != nil {
		logger.Error(
			"failed to start MCP server:",
			"error", err.Error())
	}
}
