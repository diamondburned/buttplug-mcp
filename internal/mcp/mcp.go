// Copyright (c) 2025 Neomantra BV

// Package mcp implements the model context protocol server for Buttplug.io
// device control.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/ConAcademy/buttplug-mcp/internal/bp"
	"github.com/goccy/go-yaml"
	"github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

const regexInt = `^[0-9]*$`

// Config is configuration for our MCP server.
type Config struct {
	Name        string // Service Name
	Version     string // Service Version
	UseSSE      bool   // Use SSE Transport instead of STDIO
	SSEHostPort string // HostPort to use for SSE

	// ToolDescriptionsFile is the path to a YAML file containing tool
	// descriptions to override defaults.
	ToolDescriptionsFile string

	// ButtplugWebsocketAddress is the address of the Buttplug Websocket server
	// to connect to.
	ButtplugWebsocketAddress string
}

// Server is our MCP server.
type Server struct {
	mcp      *mcpserver.MCPServer
	bpm      *bp.Manager
	patterns *bp.PatternPlayer
	logger   *slog.Logger
	config   Config
	td       ToolDescriptions
}

// New creates a new MCP server with the given configuration.
func New(config Config, bpm *bp.Manager, logger *slog.Logger) (*Server, error) {
	td := defaultToolDescriptions()
	if config.ToolDescriptionsFile != "" {
		b, err := os.ReadFile(config.ToolDescriptionsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to read tool descriptions file: %w", err)
		}
		if err := yaml.Unmarshal(b, &td); err != nil {
			return nil, fmt.Errorf("failed to parse tool descriptions file: %w", err)
		}
	}

	// Create the MCP Server
	mcp := mcpserver.NewMCPServer(
		config.Name, config.Version,
		mcpserver.WithToolCapabilities(true), // Enable tools
		mcpserver.WithInstructions(td.ForTool("_")),
	)

	s := &Server{
		mcp:      mcp,
		bpm:      bpm,
		patterns: bp.NewPatternPlayer(bpm, logger),
		logger:   logger,
		config:   config,
		td:       td,
	}
	s.registerTools()

	return s, nil
}

// ToolDescriptions returns the tool descriptions used by the server, which may
// be merged with user-provided descriptions.
func (s *Server) ToolDescriptions() ToolDescriptions {
	return s.td
}

// Start starts the MCP server.
func (s *Server) Start() error {
	if s.config.UseSSE {
		s.logger.Info("MCP SSE server starting", "hostPort", s.config.SSEHostPort)
		return mcpserver.NewSSEServer(s.mcp).Start(s.config.SSEHostPort)
	} else {
		s.logger.Info("MCP stdio server starting")
		return mcpserver.ServeStdio(s.mcp)
	}
}

// registerTools registers tools+metadata with the passed MCPServer
func (s *Server) registerTools() {
	// get_device_ids
	s.mcp.AddTool(mcp.NewTool("get_device_ids",
		mcp.WithTitleAnnotation("Get Device IDs"),
		mcp.WithDescription(s.td.ForTool("get_device_ids")),
	), s.handleDeviceListTool)

	// get_device_by_id
	s.mcp.AddTool(mcp.NewTool("get_device_by_id",
		mcp.WithTitleAnnotation("Get Device By ID"),
		mcp.WithDescription(s.td.ForTool("get_device_by_id")),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description(s.td.ForToolParameter("get_device_by_id", "id")),
			mcp.Pattern(regexInt),
		),
	), s.handleDeviceOneTool)

	// device_vibrate
	s.mcp.AddTool(mcp.NewTool("device_vibrate",
		mcp.WithTitleAnnotation("Device Vibrate"),
		mcp.WithDescription(s.td.ForTool("device_vibrate")),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description(s.td.ForToolParameter("device_vibrate", "id")),
			mcp.Pattern(regexInt),
		),
		mcp.WithArray("pattern",
			mcp.Description(s.td.ForToolParameter("device_vibrate", "pattern")),
			mcp.Required(),
			mcp.Items(map[string]any{
				"type": "object",
				"properties": map[string]any{
					"strength": convertPropertyOptions(
						mcp.Description(s.td.ForToolParameter("device_vibrate", "pattern.strength")),
						mcp.Min(0.0),
						mcp.Max(1.0),
					),
					"duration_sec": convertPropertyOptions(
						mcp.Description(s.td.ForToolParameter("device_vibrate", "pattern.duration_sec")),
						mcp.Min(0.1),
						mcp.Max(3600.0),
					),
				},
				"required": []string{
					"strength",
					"duration_sec",
				},
			}),
			mcp.MinItems(1),
		),
		mcp.WithNumber("repeats",
			mcp.Description(s.td.ForToolParameter("device_vibrate", "repeats")),
			mcp.DefaultNumber(1),
			mcp.Min(0),
		),
		mcp.WithNumber("return_after_duration_sec",
			mcp.Description(s.td.ForToolParameter("device_vibrate", "return_after_duration_sec")),
			mcp.DefaultNumber(0.0),
			mcp.Min(0.0),
		),
	), s.handleDeviceVibrate)

	// device_stop
	s.mcp.AddTool(mcp.NewTool("device_stop",
		mcp.WithTitleAnnotation("Device Stop"),
		mcp.WithDescription(s.td.ForTool("device_stop")),
	), s.handleDeviceStop)
}

func convertPropertyOptions(opts ...mcp.PropertyOption) map[string]any {
	m := make(map[string]any)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (s *Server) handleDeviceListTool(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	jbytes, err := json.Marshal(map[string]any{
		"device_ids": s.bpm.DeviceIndexes(),
	})
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(jbytes)), nil
}

func (s *Server) handleDeviceOneTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	deviceID, err := request.RequireInt("id")
	if err != nil {
		return nil, fmt.Errorf("id must be set %w", err)
	}

	device, err := s.bpm.Device(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query device %d: %w", deviceID, err)
	}

	jbytes, err := json.Marshal(map[string]any{
		"device_id": deviceID,
		"device":    device,
	})
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(jbytes)), nil
}

func (s *Server) handleDeviceVibrate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		DeviceID               int        `json:"id"`
		Pattern                bp.Pattern `json:"pattern"`
		Repeats                int        `json:"repeats"`
		ReturnAfterDurationSec float64    `json:"return_after_duration_sec"`
	}

	if err := request.BindArguments(&args); err != nil {
		return nil, fmt.Errorf("failed to bind arguments: %w", err)
	}

	if err := s.patterns.Play(ctx, args.DeviceID, args.Pattern, args.Repeats); err != nil {
		return nil, fmt.Errorf("failed to play pattern on device %d: %w", args.DeviceID, err)
	}

	if args.ReturnAfterDurationSec <= 0 {
		return mcp.NewToolResultText(`{ "success": true, "waited": false }`), nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(args.ReturnAfterDurationSec * float64(time.Second))):
		return mcp.NewToolResultText(`{ "success": true, "waited": true }`), nil
	}
}

func (s *Server) handleDeviceStop(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// always stop any running pattern
	s.patterns.StopAll()

	if err := s.bpm.StopAll(ctx); err != nil {
		return nil, fmt.Errorf("failed to stop all devices: %w", err)
	}

	return mcp.NewToolResultText(`{ "success": true }`), nil
}
