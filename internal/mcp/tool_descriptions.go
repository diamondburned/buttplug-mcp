package mcp

import (
	_ "embed"
	"fmt"

	"github.com/goccy/go-yaml"
)

// ToolDescriptions is a map of tool names to their descriptions and parameters.
// The user may override these descriptions to better guide the agent to use the
// tool to their needs.
type ToolDescriptions struct {
	Server   ServerInstructions         `yaml:"_"`
	Tools    map[string]ToolDescription `yaml:",inline"`
	comments yaml.CommentMap            `yaml:"-"`
}

// ForTool returns the tool description for the given tool name, or panics if
// not found.
func (d ToolDescriptions) ForTool(name string) string {
	desc, ok := d.Tools[name]
	if !ok {
		panic(fmt.Sprintf("tool description not found for tool: %s", name))
	}
	return desc.Description
}

// ForToolParameter returns the parameter description for the given tool name
// and parameter name, or panics if not found.
func (d ToolDescriptions) ForToolParameter(toolName, paramName string) string {
	descTool, ok := d.Tools[toolName]
	if !ok {
		panic(fmt.Sprintf("tool description not found for tool: %s", toolName))
	}
	desc, ok := descTool.Parameters[paramName]
	if !ok {
		panic(fmt.Sprintf("parameter description not found for tool: %s, parameter: %s", toolName, paramName))
	}
	return desc.Description
}

// ServerInstructions holds general instructions for the MCP server.
type ServerInstructions struct {
	Instructions string `yaml:"instructions"`
}

// ToolDescription describes the parameters and description of a tool.
type ToolDescription struct {
	Description string                          `yaml:"description"`
	Parameters  map[string]ParameterDescription `yaml:"parameters"`
}

// ParameterDescription describes a single parameter for a tool.
type ParameterDescription struct {
	Description string `yaml:"description"`
}

//go:embed tool_descriptions.yaml
var defaultToolDescriptionsYAML []byte

func defaultToolDescriptions() ToolDescriptions {
	var td ToolDescriptions
	if err := parseToolDescriptionsFromYAML(defaultToolDescriptionsYAML, &td); err != nil {
		panic(fmt.Sprintf("BUG: failed to parse embedded default tool descriptions: %v", err))
	}
	return td
}

func parseToolDescriptionsFromYAML(b []byte, dst *ToolDescriptions) error {
	if dst.comments == nil {
		dst.comments = make(yaml.CommentMap)
	}
	return yaml.UnmarshalWithOptions(b, dst,
		yaml.CommentToMap(dst.comments))
}

// FormatToolDescriptionsAsYAML formats the tool descriptions as a
// pretty-printed YAML blob.
func FormatToolDescriptionsAsYAML(td ToolDescriptions) ([]byte, error) {
	return yaml.MarshalWithOptions(td,
		yaml.Indent(2),
		yaml.OmitEmpty(),
		yaml.WithComment(td.comments),
		yaml.UseLiteralStyleIfMultiline(true),
	)
}
