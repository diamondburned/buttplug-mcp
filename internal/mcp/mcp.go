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
	"regexp"
	"strconv"
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
	mcp    *mcpserver.MCPServer
	bpm    *bp.Manager
	logger *slog.Logger
	config Config
	td     ToolDescriptions
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
		mcp:    mcp,
		bpm:    bpm,
		logger: logger,
		config: config,
		td:     td,
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
		mcp.WithNumber("strength",
			mcp.Required(),
			mcp.Description(s.td.ForToolParameter("device_vibrate", "strength")),
			mcp.DefaultNumber(1.0),
			mcp.Min(0.0),
			mcp.Max(1.0),
		),
		mcp.WithNumber("duration_sec",
			mcp.Required(),
			mcp.Description(s.td.ForToolParameter("device_vibrate", "duration_sec")),
			mcp.DefaultNumber(0.0),
			mcp.Min(0.0),
			mcp.Max(3600.0),
		),
		mcp.WithBoolean("stop_after_duration",
			mcp.Description(s.td.ForToolParameter("device_vibrate", "stop_after_duration")),
			mcp.DefaultBool(true),
		),
	), s.handleDeviceVibrate)

	// device_stop
	s.mcp.AddTool(mcp.NewTool("device_stop",
		mcp.WithTitleAnnotation("Device Stop"),
		mcp.WithDescription(s.td.ForTool("device_stop")),
		mcp.WithNumber("id",
			mcp.Required(),
			mcp.Description(s.td.ForToolParameter("device_stop", "id")),
			mcp.Pattern(regexInt),
		),
	), s.handleDeviceStop)
}

func (s *Server) handleDeviceList(_ context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	jbytes, err := s.handleDeviceListRaw()
	if err != nil {
		return nil, err
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(jbytes),
		},
	}, nil
}

func (s *Server) handleDeviceListTool(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	jbytes, err := s.handleDeviceListRaw()
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(jbytes)), nil
}

func (s *Server) handleDeviceListRaw() (json.RawMessage, error) {
	return json.Marshal(map[string]any{
		"device_ids": s.bpm.DeviceIndexes(),
	})
}

var reDeviceID = regexp.MustCompile(`/device/(\d+)`)

func (s *Server) handleDeviceOne(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	deviceIDMatch := reDeviceID.FindStringSubmatch(request.Params.URI)
	if deviceIDMatch == nil {
		return nil, fmt.Errorf("invalid device ID in URI")
	}

	deviceID, err := strconv.Atoi(deviceIDMatch[1])
	if err != nil {
		return nil, fmt.Errorf("invalid device ID: %w", err)
	}

	jbytes, err := s.handleDeviceOneRaw(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	return []mcp.ResourceContents{
		&mcp.TextResourceContents{
			URI:      request.Params.URI,
			MIMEType: "application/json",
			Text:     string(jbytes),
		},
	}, nil
}

func (s *Server) handleDeviceOneTool(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	deviceID, err := request.RequireInt("id")
	if err != nil {
		return nil, fmt.Errorf("id must be set %w", err)
	}

	jbytes, err := s.handleDeviceOneRaw(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	return mcp.NewToolResultText(string(jbytes)), nil
}

func (s *Server) handleDeviceOneRaw(ctx context.Context, deviceID int) (json.RawMessage, error) {
	device, err := s.bpm.Device(ctx, deviceID)
	if err != nil {
		return nil, fmt.Errorf("failed to query device %d: %w", deviceID, err)
	}

	return json.Marshal(map[string]any{
		"device_id": deviceID,
		"device":    device,
	})
}

func (s *Server) handleDeviceVibrate(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	deviceID, err := request.RequireInt("id")
	if err != nil {
		return nil, fmt.Errorf("id must be set %w", err)
	}

	strength, err := request.RequireFloat("strength")
	if err != nil {
		return nil, fmt.Errorf("strength must be set %w", err)
	}

	durationSec, err := request.RequireFloat("duration_sec")
	if err != nil {
		return nil, fmt.Errorf("duration_sec must be set %w", err)
	}

	stopAfterDuration, err := request.RequireBool("stop_after_duration")
	if err != nil {
		return nil, fmt.Errorf("stop_after_duration must be set %w", err)
	}

	if err := s.bpm.DeviceVibrate(ctx, deviceID, strength); err != nil {
		return nil, fmt.Errorf("failed to vibrate device %d: %w", deviceID, err)
	}

	if durationSec == 0 {
		return mcp.NewToolResultText(`{ "success": true, "stopped": false }`), nil
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(durationSec * float64(time.Second))):
	}

	if stopAfterDuration {
		if err := s.bpm.DeviceStop(ctx, deviceID); err != nil {
			return nil, fmt.Errorf("failed to stop device %d after duration: %w", deviceID, err)
		}

		return mcp.NewToolResultText(`{ "success": true, "stopped": true }`), nil
	} else {
		return mcp.NewToolResultText(`{ "success": true, "stopped": false }`), nil
	}
}

func (s *Server) handleDeviceStop(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	deviceID, err := request.RequireInt("id")
	if err != nil {
		return nil, fmt.Errorf("id must be set %w", err)
	}

	if err := s.bpm.DeviceStop(ctx, deviceID); err != nil {
		return nil, fmt.Errorf("failed to stop device %d: %w", deviceID, err)
	}

	return mcp.NewToolResultText(`{ "success": true }`), nil
}
