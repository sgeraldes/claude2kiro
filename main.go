package main

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	jsonStr "encoding/json"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/manifoldco/promptui"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	"github.com/sgeraldes/claude2kiro/internal/debug"
	"github.com/sgeraldes/claude2kiro/internal/sse"
	"github.com/sgeraldes/claude2kiro/internal/tui"
	"github.com/sgeraldes/claude2kiro/internal/tui/dashboard"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
	"github.com/sgeraldes/claude2kiro/parser"
)

// TokenData represents the token file structure
type TokenData struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
	AuthMethod   string `json:"authMethod,omitempty"`   // "social" or "IdC"
	Provider     string `json:"provider,omitempty"`     // "GitHub", "Google", "BuilderId", "Enterprise"
	ProfileArn   string `json:"profileArn,omitempty"`   // For social auth
	ClientIdHash string `json:"clientIdHash,omitempty"` // For IdC auth - hash of start URL
	Region       string `json:"region,omitempty"`       // For IdC auth - AWS region
	StartUrl     string `json:"startUrl,omitempty"`     // For IdC Enterprise - the start URL
}

// CreateTokenRequest represents the request to exchange auth code for tokens
type CreateTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectUri  string `json:"redirect_uri"`
}

// CreateTokenResponse represents the response from token exchange (Kiro social auth uses camelCase)
type CreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ProfileArn   string `json:"profileArn,omitempty"`
	ExpiresIn    int    `json:"expiresIn"`
}

// SSO OIDC types for Identity Center authentication

// SSOClientRegistration represents cached client registration
type SSOClientRegistration struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	ExpiresAt    string `json:"expiresAt"`
}

// SSORegisterClientRequest represents SSO OIDC register client request
type SSORegisterClientRequest struct {
	ClientName   string   `json:"clientName"`
	ClientType   string   `json:"clientType"`
	Scopes       []string `json:"scopes"`
	GrantTypes   []string `json:"grantTypes"`
	RedirectUris []string `json:"redirectUris"`
	IssuerUrl    string   `json:"issuerUrl"`
}

// SSORegisterClientResponse represents SSO OIDC register client response
type SSORegisterClientResponse struct {
	ClientId              string `json:"clientId"`
	ClientSecret          string `json:"clientSecret"`
	ClientSecretExpiresAt int64  `json:"clientSecretExpiresAt"`
}

// SSOCreateTokenRequest represents SSO OIDC create token request
type SSOCreateTokenRequest struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	Code         string `json:"code,omitempty"`
	RedirectUri  string `json:"redirectUri,omitempty"`
	CodeVerifier string `json:"codeVerifier,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// SSOCreateTokenResponse represents SSO OIDC create token response
// AWS SSO OIDC returns camelCase JSON fields
type SSOCreateTokenResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType"`
}

// LoginConfig stores the last used login method and parameters
type LoginConfig struct {
	Method   string `json:"method"`             // github, google, builderid, idc
	StartUrl string `json:"startUrl,omitempty"` // For idc method
	Region   string `json:"region,omitempty"`   // For idc method
}

// RefreshRequest represents the token refresh request structure (Kiro uses camelCase)
type RefreshRequest struct {
	RefreshToken string `json:"refreshToken"`
}

// RefreshResponse represents the token refresh response structure (Kiro uses camelCase)
type RefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt,omitempty"`
}

// AnthropicTool represents the Anthropic API tool structure
type AnthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// InputSchema represents the tool input schema structure
type InputSchema struct {
	Json map[string]any `json:"json"`
}

// ToolSpecification represents the tool specification structure
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// CodeWhispererTool represents the CodeWhisperer API tool structure
type CodeWhispererTool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// HistoryUserMessage represents a user message in conversation history
type HistoryUserMessage struct {
	UserInputMessage struct {
		Content string `json:"content"`
		ModelId string `json:"modelId"`
		Origin  string `json:"origin"`
	} `json:"userInputMessage"`
}

// HistoryAssistantMessage represents an assistant message in conversation history
type HistoryAssistantMessage struct {
	AssistantResponseMessage struct {
		Content  string `json:"content"`
		ToolUses []any  `json:"toolUses"`
	} `json:"assistantResponseMessage"`
}

// AnthropicRequest represents the Anthropic API request structure
type AnthropicRequest struct {
	Model       string                    `json:"model"`
	MaxTokens   int                       `json:"max_tokens"`
	Messages    []AnthropicRequestMessage `json:"messages"`
	System      []AnthropicSystemMessage  `json:"system,omitempty"`
	Tools       []AnthropicTool           `json:"tools,omitempty"`
	Stream      bool                      `json:"stream"`
	Temperature *float64                  `json:"temperature,omitempty"`
	Metadata    map[string]any            `json:"metadata,omitempty"`
}

// CapturedSSEEvent represents a captured SSE event for comparison mode
type CapturedSSEEvent struct {
	Event string `json:"event"`
	Data  string `json:"data"`
}

// AnthropicStreamResponse represents the Anthropic streaming response structure
type AnthropicStreamResponse struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentDelta struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"delta,omitempty"`
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage,omitempty"`
}

// AnthropicRequestMessage represents the Anthropic API message structure
type AnthropicRequestMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // Can be string or []ContentBlock
}

type AnthropicSystemMessage struct {
	Type string `json:"type"`
	Text string `json:"text"` // Can be string or []ContentBlock
}

// ContentBlock represents the message content block structure
type ContentBlock struct {
	Type      string        `json:"type"`
	Text      *string       `json:"text,omitempty"`
	ToolUseId *string       `json:"tool_use_id,omitempty"`
	Content   *string       `json:"content,omitempty"`
	Name      *string       `json:"name,omitempty"`
	Input     *any          `json:"input,omitempty"`
	Source    *ImageSource  `json:"source,omitempty"` // For image content blocks
}

// ImageSource represents the source of an image in Anthropic API format
type ImageSource struct {
	Type      string `json:"type"`       // "base64" or "url"
	MediaType string `json:"media_type"` // e.g., "image/png", "image/jpeg"
	Data      string `json:"data"`       // base64-encoded image data
}

// sanitizeHistoryContent cleans up corrupted content that was produced by
// earlier buggy versions of the proxy (e.g., "answer for user question" placeholder)
func sanitizeHistoryContent(content string) string {
	// Known garbage content that was produced by earlier bugs
	garbageContent := []string{
		"answer for user question",
		"(no content)",
	}
	for _, garbage := range garbageContent {
		if content == garbage {
			return "" // Replace with empty string
		}
	}
	return content
}

// getMessageContent extracts text content from a message
func getMessageContent(content any) string {
	switch v := content.(type) {
	case string:
		if len(v) == 0 {
			return "" // Empty content is valid
		}
		return v
	case []interface{}:
		var texts []string
		hasToolUse := false
		hasToolResult := false
		for _, block := range v {
			if m, ok := block.(map[string]interface{}); ok {
				// Get the block type
				blockType, _ := m["type"].(string)
				switch blockType {
				case "tool_result":
					hasToolResult = true
					// Tool result content can be a string or an array of content blocks
					if contentStr, ok := m["content"].(string); ok {
						texts = append(texts, contentStr)
					} else if contentArr, ok := m["content"].([]interface{}); ok {
						// Content is an array of blocks - extract text from each
						for _, innerBlock := range contentArr {
							if innerMap, ok := innerBlock.(map[string]interface{}); ok {
								if innerType, _ := innerMap["type"].(string); innerType == "text" {
									if textVal, ok := innerMap["text"].(string); ok {
										texts = append(texts, textVal)
									}
								}
							}
						}
					}
				case "text":
					if textVal, ok := m["text"].(string); ok {
						texts = append(texts, textVal)
					}
				case "tool_use":
					// Tool use blocks are handled separately in ToolUses array
					// Just mark that we have tool use for proper content handling
					hasToolUse = true
				}
			}
		}
		_ = hasToolResult // Used for debugging if needed
		if len(texts) == 0 {
			if hasToolUse {
				// Tool-only message - return empty content
				// The tool information will be in the ToolUses array
				return ""
			}
			// No content found - return empty instead of placeholder
			return ""
		}
		return strings.Join(texts, "\n")
	default:
		// Unknown type - return empty instead of placeholder
		return ""
	}
}

// HistoryToolUse represents a tool use in conversation history
type HistoryToolUse struct {
	Name      string `json:"name"`
	ToolUseId string `json:"toolUseId"`
	Input     any    `json:"input"` // JSON object of the input
}

// getMessageToolUses extracts tool_use blocks from a message content
func getMessageToolUses(content any) []HistoryToolUse {
	var toolUses []HistoryToolUse

	blocks, ok := content.([]interface{})
	if !ok {
		return toolUses
	}

	for _, block := range blocks {
		if m, ok := block.(map[string]interface{}); ok {
			if typeVal, ok := m["type"].(string); ok && typeVal == "tool_use" {
				toolUse := HistoryToolUse{}
				if name, ok := m["name"].(string); ok {
					toolUse.Name = name
				}
				if id, ok := m["id"].(string); ok {
					toolUse.ToolUseId = id
				}
				if input, ok := m["input"]; ok {
					// Keep input as-is (JSON object), don't convert to string
					toolUse.Input = input
				}
				if toolUse.Name != "" && toolUse.ToolUseId != "" {
					toolUses = append(toolUses, toolUse)
				}
			}
		}
	}

	return toolUses
}

// ToolResultContent represents content in a tool result (text or image)
type ToolResultContent struct {
	Text  string                  `json:"text,omitempty"`
	Image *ToolResultImageContent `json:"image,omitempty"`
}

// ToolResultImageContent represents an image in a tool result
type ToolResultImageContent struct {
	Format string `json:"format"` // "png", "jpeg", "gif", "webp"
	Source struct {
		Bytes string `json:"bytes"` // base64-encoded image data
	} `json:"source"`
}

// ToolResult represents a single tool result
type ToolResult struct {
	Content   []ToolResultContent `json:"content"`
	Status    string              `json:"status"`
	ToolUseId string              `json:"toolUseId"`
}

// ImageBlock represents an image attachment for CodeWhisperer API
type ImageBlock struct {
	Format string `json:"format"` // "png", "jpeg", "gif", "webp"
	Source struct {
		Bytes string `json:"bytes"` // base64-encoded image data
	} `json:"source"`
}

// CodeWhispererRequest represents the CodeWhisperer API request structure
type CodeWhispererRequest struct {
	ConversationState struct {
		ChatTriggerType string `json:"chatTriggerType"`
		ConversationId  string `json:"conversationId"`
		CurrentMessage  struct {
			UserInputMessage struct {
				Content                 string `json:"content"`
				ModelId                 string `json:"modelId"`
				Origin                  string `json:"origin"`
				Images                  []ImageBlock `json:"images,omitempty"`
				UserInputMessageContext struct {
					ToolResults []ToolResult        `json:"toolResults,omitempty"`
					Tools       []CodeWhispererTool `json:"tools,omitempty"`
				} `json:"userInputMessageContext"`
			} `json:"userInputMessage"`
		} `json:"currentMessage"`
		History []any `json:"history"`
	} `json:"conversationState"`
	ProfileArn string `json:"profileArn,omitempty"`
}

// CodeWhispererEvent represents the CodeWhisperer event response
type CodeWhispererEvent struct {
	ContentType string `json:"content-type"`
	MessageType string `json:"message-type"`
	Content     string `json:"content"`
	EventType   string `json:"event-type"`
}

// tokenRefreshMutex prevents concurrent token refresh operations
var tokenRefreshMutex sync.Mutex

// kiroRequestSema limits concurrent requests to Kiro backend (some 400s may be concurrency-related)
var kiroRequestSema = make(chan struct{}, 2) // Allow max 2 concurrent requests

// ModelMap translates Anthropic model IDs (sent by Claude Code) to Kiro model IDs.
// Kiro model IDs discovered via GET /ListAvailableModels?origin=AI_EDITOR
var ModelMap = map[string]string{
	// Auto - lets Kiro choose the best model (1.0x credits)
	"auto": "auto",
	// Claude Opus 4.6 (2.2x credits, 1M context)
	"claude-opus-4-6": "claude-opus-4.6",
	"claude-opus-4.6": "claude-opus-4.6",
	// Claude Opus 4.5 (2.2x credits, 200K context)
	"claude-opus-4-5-20251101": "claude-opus-4.5",
	"claude-opus-4-5":          "claude-opus-4.5",
	"claude-opus-4.5":          "claude-opus-4.5",
	// Claude Opus 4.1 -> mapped to Opus 4.5 (closest available)
	"claude-opus-4-1-20250805": "claude-opus-4.5",
	"claude-opus-4-1":          "claude-opus-4.5",
	"claude-opus-4.1":          "claude-opus-4.5",
	// Claude Opus 4.0 -> mapped to Opus 4.5
	"claude-opus-4-20250514": "claude-opus-4.5",
	// Claude Sonnet 4.6 (1.3x credits, 1M context)
	"claude-sonnet-4-6": "claude-sonnet-4.6",
	"claude-sonnet-4.6": "claude-sonnet-4.6",
	// Claude Sonnet 4.5 (1.3x credits, 200K context)
	"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
	"claude-sonnet-4-5":          "claude-sonnet-4.5",
	"claude-sonnet-4.5":          "claude-sonnet-4.5",
	// Claude Sonnet 4.0 (1.3x credits, 200K context)
	"claude-sonnet-4-20250514": "claude-sonnet-4",
	// Claude Sonnet 3.7 -> mapped to Sonnet 4.5
	"claude-3-7-sonnet-20250219": "claude-sonnet-4.5",
	// Claude Sonnet 3.5 v2 -> mapped to Sonnet 4.5
	"claude-3-5-sonnet-20241022": "claude-sonnet-4.5",
	// Claude Haiku 4.5 (0.4x credits, 200K context)
	"claude-haiku-4-5-20251001": "claude-haiku-4.5",
	"claude-haiku-4-5":          "claude-haiku-4.5",
	"claude-haiku-4.5":          "claude-haiku-4.5",
	"claude-3-5-haiku-20241022": "claude-haiku-4.5",
	// Non-Claude models (accessible via --model flag or ANTHROPIC_CUSTOM_MODEL_OPTION)
	// DeepSeek 3.2 (0.25x credits, 164K context)
	"deepseek-3.2":    "deepseek-3.2",
	"deepseek-3-2":    "deepseek-3.2",
	"deepseek-v3.2":   "deepseek-3.2",
	"deepseek":        "deepseek-3.2",
	// MiniMax M2.5 (0.25x credits, 196K context, text only)
	"minimax-m2.5":    "minimax-m2.5",
	"minimax-m2-5":    "minimax-m2.5",
	"minimax":         "minimax-m2.5",
	// MiniMax M2.1 (0.15x credits, 196K context)
	"minimax-m2.1":    "minimax-m2.1",
	"minimax-m2-1":    "minimax-m2.1",
	// Qwen3 Coder Next (0.05x credits, 256K context)
	"qwen3-coder-next": "qwen3-coder-next",
	"qwen3":            "qwen3-coder-next",
	"qwen":             "qwen3-coder-next",
}

// getKiroModelID converts an Anthropic model name to Kiro model ID
// First checks the static map, then falls back to family-based mapping
func getKiroModelID(anthropicModel string) string {
	// Check static map first
	if kiroModel, ok := ModelMap[anthropicModel]; ok {
		return kiroModel
	}

	// Family-based fallback: map unknown versions to the latest known Kiro model
	lower := strings.ToLower(anthropicModel)
	switch {
	case strings.Contains(lower, "opus"):
		return "claude-opus-4.6"
	case strings.Contains(lower, "haiku"):
		return "claude-haiku-4.5"
	case strings.Contains(lower, "sonnet"):
		return "claude-sonnet-4.6"
	case strings.Contains(lower, "deepseek"):
		return "deepseek-3.2"
	case strings.Contains(lower, "minimax"):
		return "minimax-m2.5"
	case strings.Contains(lower, "qwen"):
		return "qwen3-coder-next"
	}

	// Last resort: pass through as-is (Kiro may accept it)
	return anthropicModel
}

// generateUUID generates a simple UUID v4
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Fallback to math/rand if crypto/rand fails (should never happen)
		for i := range b {
			b[i] = byte(mathrand.Intn(256))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant bits
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// extractImagesFromContent extracts images from Anthropic message content
func extractImagesFromContent(content any) []ImageBlock {
	var images []ImageBlock

	contentBlocks, ok := content.([]interface{})
	if !ok {
		return images
	}

	for _, block := range contentBlocks {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)
		if blockType != "image" {
			continue
		}

		source, ok := blockMap["source"].(map[string]interface{})
		if !ok {
			continue
		}

		sourceType, _ := source["type"].(string)
		if sourceType != "base64" {
			continue
		}

		mediaType, _ := source["media_type"].(string)
		data, _ := source["data"].(string)

		if data == "" {
			continue
		}

		// Convert media type to format (e.g., "image/png" -> "png")
		format := "png" // default
		switch mediaType {
		case "image/png":
			format = "png"
		case "image/jpeg", "image/jpg":
			format = "jpeg"
		case "image/gif":
			format = "gif"
		case "image/webp":
			format = "webp"
		}

		imgBlock := ImageBlock{
			Format: format,
		}
		imgBlock.Source.Bytes = data
		images = append(images, imgBlock)
	}

	return images
}

// buildCodeWhispererRequest builds a CodeWhisperer request from an Anthropic request
func buildCodeWhispererRequest(anthropicReq AnthropicRequest, token TokenData) CodeWhispererRequest {
	cwReq := CodeWhispererRequest{}

	// Set ProfileArn based on auth method
	if token.AuthMethod == "IdC" {
		// For IdC, omit ProfileArn - the token itself identifies the user
		cwReq.ProfileArn = ""
	} else {
		// For social auth (GitHub/Google), use the profileArn from the token response
		// or fall back to the hardcoded Kiro consumer profile
		if token.ProfileArn != "" {
			cwReq.ProfileArn = token.ProfileArn
		} else {
			cwReq.ProfileArn = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"
		}
	}
	cwReq.ConversationState.ChatTriggerType = "MANUAL"
	cwReq.ConversationState.ConversationId = generateUUID()
	cwReq.ConversationState.CurrentMessage.UserInputMessage.Content = getMessageContent(anthropicReq.Messages[len(anthropicReq.Messages)-1].Content)
	cwReq.ConversationState.CurrentMessage.UserInputMessage.ModelId = getKiroModelID(anthropicReq.Model)
	cwReq.ConversationState.CurrentMessage.UserInputMessage.Origin = "AI_EDITOR"
	// Process tools information
	// CodeWhisperer has a ~10KB limit on tool descriptions
	const maxToolDescLength = 10000
	if len(anthropicReq.Tools) > 0 {
		var tools []CodeWhispererTool
		for _, tool := range anthropicReq.Tools {
			cwTool := CodeWhispererTool{}
			cwTool.ToolSpecification.Name = tool.Name
			// Truncate long descriptions to avoid 400 errors
			desc := tool.Description
			if len(desc) > maxToolDescLength {
				desc = desc[:maxToolDescLength] + "...(truncated)"
			}
			cwTool.ToolSpecification.Description = desc
			cwTool.ToolSpecification.InputSchema = InputSchema{
				Json: tool.InputSchema,
			}
			tools = append(tools, cwTool)
		}
		cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools = tools
	}

	// Extract images from the current message and add directly to UserInputMessage
	if len(anthropicReq.Messages) > 0 {
		lastMsg := anthropicReq.Messages[len(anthropicReq.Messages)-1]
		images := extractImagesFromContent(lastMsg.Content)
		if len(images) > 0 {
			cwReq.ConversationState.CurrentMessage.UserInputMessage.Images = images
		}
	}

	// Build conversation history
	// Process system messages or regular history messages
	if len(anthropicReq.System) > 0 || len(anthropicReq.Messages) > 1 {
		var history []any

		// Add each system message as a separate history entry

		assistantDefaultMsg := HistoryAssistantMessage{}
		assistantDefaultMsg.AssistantResponseMessage.Content = getMessageContent("I will follow these instructions")
		assistantDefaultMsg.AssistantResponseMessage.ToolUses = make([]any, 0)

		if len(anthropicReq.System) > 0 {
			for _, sysMsg := range anthropicReq.System {
				userMsg := HistoryUserMessage{}
				userMsg.UserInputMessage.Content = sysMsg.Text
				userMsg.UserInputMessage.ModelId = getKiroModelID(anthropicReq.Model)
				userMsg.UserInputMessage.Origin = "AI_EDITOR"
				history = append(history, userMsg)
				history = append(history, assistantDefaultMsg)
			}
		}

		// Then process regular message history
		for i := 0; i < len(anthropicReq.Messages)-1; i++ {
			if anthropicReq.Messages[i].Role == "user" {
				userContent := sanitizeHistoryContent(getMessageContent(anthropicReq.Messages[i].Content))

				userMsg := HistoryUserMessage{}
				userMsg.UserInputMessage.Content = userContent
				userMsg.UserInputMessage.ModelId = getKiroModelID(anthropicReq.Model)
				userMsg.UserInputMessage.Origin = "AI_EDITOR"
				history = append(history, userMsg)

				// Check if the next message is an assistant reply
				if i+1 < len(anthropicReq.Messages)-1 && anthropicReq.Messages[i+1].Role == "assistant" {
					content := sanitizeHistoryContent(getMessageContent(anthropicReq.Messages[i+1].Content))

					assistantMsg := HistoryAssistantMessage{}
					assistantMsg.AssistantResponseMessage.Content = content
					assistantMsg.AssistantResponseMessage.ToolUses = make([]any, 0)
					history = append(history, assistantMsg)
					i++ // Skip the already processed assistant message
				}
			}
		}

		cwReq.ConversationState.History = history
	}

	return cwReq
}

func main() {
	if len(os.Args) < 2 {
		// No arguments - launch TUI
		runTUI()
		return
	}

	command := os.Args[1]

	switch command {
	case "login":
		var config *LoginConfig

		// If no method specified, show interactive menu or use saved config
		if len(os.Args) == 2 {
			savedConfig, err := readLoginConfig()
			// Only offer to reuse saved config if there's also a valid token
			// (meaning the previous login completed successfully)
			if err == nil && savedConfig.Method != "" && hasValidToken() {
				// Has saved config AND valid token - show details and ask to confirm
				methodDesc := savedConfig.Method
				if savedConfig.Method == "idc" && savedConfig.StartUrl != "" {
					methodDesc = fmt.Sprintf("%s (%s, %s)", savedConfig.Method, savedConfig.StartUrl, savedConfig.Region)
				}
				fmt.Printf("Last login: %s\n", methodDesc)

				confirmPrompt := promptui.Prompt{
					Label:     "Use saved settings",
					IsConfirm: true,
				}
				_, err := confirmPrompt.Run()
				if err == nil {
					config = savedConfig
				} else {
					// User said no, show interactive menu
					config = interactiveLogin()
				}
			} else {
				// No saved config OR no valid token - show interactive menu
				config = interactiveLogin()
			}

			// Save the config for future use
			if err := saveLoginConfig(config); err != nil {
				fmt.Printf("Warning: Failed to save login config: %v\n", err)
			}
		} else {
			// Method specified via CLI args, parse and save it
			method := strings.ToLower(os.Args[2])
			config = &LoginConfig{Method: method}

			if method == "idc" {
				if len(os.Args) < 4 {
					// No URL provided, launch interactive IdC login
					config = interactiveIdCLogin()
				} else {
					config.StartUrl = normalizeStartUrl(os.Args[3])
					config.Region = "us-east-1"
					if len(os.Args) > 4 {
						config.Region = os.Args[4]
					}
				}
			}

			// Save the config for future use
			if err := saveLoginConfig(config); err != nil {
				fmt.Printf("Warning: Failed to save login config: %v\n", err)
			}
		}

		// Execute login based on config
		switch config.Method {
		case "github":
			loginSocial("Github")
		case "google":
			loginSocial("Google")
		case "builderid":
			loginIdC("BuilderId", "https://view.awsapps.com/start", "us-east-1")
		case "idc":
			if config.StartUrl == "" {
				fmt.Println("Error: No start URL configured for idc method")
				fmt.Println("Usage: claude2kiro login idc <start-url> [region]")
				os.Exit(1)
			}
			region := config.Region
			if region == "" {
				region = "us-east-1"
			}
			loginIdC("Enterprise", config.StartUrl, region)
		default:
			fmt.Printf("Unknown login method: %s\n", config.Method)
			fmt.Println("Use: github, google, builderid, or idc <start-url>")
			os.Exit(1)
		}
	case "read":
		readToken()
	case "refresh":
		refreshToken()
	case "export":
		exportEnvVars()
	case "claude":
		setClaude()
	case "run":
		runClaudeWithProxy()
	case "server":
		port := "8080" // Default port
		if len(os.Args) > 2 {
			port = os.Args[2]
		}
		startServer(port)
	case "migrate-logs":
		dateFilter := ""
		if len(os.Args) > 2 {
			dateFilter = os.Args[2]
		}
		if err := cmd.MigrateLogs(dateFilter); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "test":
		testProxy()
	case "logout":
		logout()
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Printf("Unknown command: %s\n", command)
		printUsage()
		os.Exit(1)
	}
}

// ensureApiKeyApproved adds the proxy's API key to Claude Code's approved list in ~/.claude.json
// so the "Detected a custom API key" prompt is skipped. This is a minimal, non-destructive change:
// it only appends to the customApiKeyResponses.approved array if not already present.
func ensureApiKeyApproved() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	claudePath := filepath.Join(homeDir, ".claude.json")
	var cfg map[string]any

	if data, err := os.ReadFile(claudePath); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg == nil {
		cfg = make(map[string]any)
	}

	// Claude Code uses the last 20 chars of ANTHROPIC_API_KEY to identify it
	const proxyKeyTail = "claude2kiro" // our ANTHROPIC_API_KEY value (< 20 chars, used as-is)

	// Get or create customApiKeyResponses.approved
	responses, _ := cfg["customApiKeyResponses"].(map[string]any)
	if responses == nil {
		responses = make(map[string]any)
	}

	approved, _ := responses["approved"].([]any)
	for _, v := range approved {
		if s, ok := v.(string); ok && s == proxyKeyTail {
			return // already approved
		}
	}

	approved = append(approved, proxyKeyTail)
	responses["approved"] = approved
	cfg["customApiKeyResponses"] = responses

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(claudePath, data, 0600)
}

// runClaudeWithProxy starts the proxy in-process, launches claude with env vars, and shuts down when claude exits.
// Usage: claude2kiro run [claude args...]
// Only minimal change to ~/.claude.json: pre-approves the proxy API key to skip the confirmation prompt.
func runClaudeWithProxy() {
	// 1. Verify we have a valid token (refresh if needed)
	token, err := getToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "No token found. Run 'claude2kiro login' first.\n")
		os.Exit(1)
	}
	_ = token // token validity is checked by getToken's proactive refresh

	// 1b. Pre-approve the proxy API key so Claude doesn't show the confirmation prompt
	ensureApiKeyApproved()

	// 2. Listen on a random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start proxy listener: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// 3. Build the proxy HTTP server (reuses the headless startServer logic)
	mux := http.NewServeMux()
	cfg := config.Get()

	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Only POST requests are supported", http.StatusMethodNotAllowed)
			return
		}

		tok, err := getToken()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get token: %v", err), http.StatusInternalServerError)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed to read request body", http.StatusBadRequest)
			return
		}

		var anthropicReq AnthropicRequest
		if err := json.Unmarshal(body, &anthropicReq); err != nil {
			http.Error(w, "Invalid JSON in request body", http.StatusBadRequest)
			return
		}

		if anthropicReq.Model == "" || len(anthropicReq.Messages) == 0 {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"Missing required field: model or messages"}}`, http.StatusBadRequest)
			return
		}

		// Check for Anthropic direct bypass
		if cfg.Advanced.AnthropicDirect {
			if cfg.Advanced.AnthropicApiKey == "" {
				http.Error(w, "anthropic_direct mode requires anthropic_api_key in config", http.StatusInternalServerError)
				return
			}
			resp, fwdErr := forwardToAnthropicHeadless(r, body)
			if fwdErr != nil {
				http.Error(w, fmt.Sprintf("Anthropic forward failed: %v", fwdErr), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()
			for k, v := range resp.Header {
				for _, val := range v {
					w.Header().Add(k, val)
				}
			}
			w.WriteHeader(resp.StatusCode)
			io.Copy(w, resp.Body)
			return
		}

		if anthropicReq.Stream {
			handleStreamRequest(w, anthropicReq, tok)
		} else {
			handleNonStreamRequest(w, anthropicReq, tok)
		}
	})

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok"}`)
	})

	mux.HandleFunc("/credits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		info := cmd.GetCreditsInfo()
		if info.Error != nil {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprintf(w, `{"error":"%s"}`, info.Error.Error())
			return
		}
		fmt.Fprintf(w, `{"used":%.1f,"limit":%.0f,"remaining":%.1f,"days_until_reset":%d,"plan":"%s"}`,
			info.CreditsUsed, info.CreditsLimit, info.CreditsRemaining, info.DaysUntilReset, info.SubscriptionName)
	})

	server := &http.Server{Handler: mux}

	// 4. Start proxy in background goroutine
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Proxy server error: %v\n", err)
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	fmt.Printf("Proxy listening on %s\n", baseURL)

	// 5. Build claude command with remaining args
	claudeArgs := os.Args[2:] // everything after "run"
	claudeCmd := exec.Command("claude", claudeArgs...)

	// Inherit current env + override API vars for this session only.
	// Use ANTHROPIC_AUTH_TOKEN (not ANTHROPIC_API_KEY) to avoid the
	// "Auth conflict: Both a token (claude.ai) and an API key" warning
	// that appears when the user is also logged into claude.ai.
	// CLAUDE2KIRO env var signals to statusline scripts that this session uses Kiro proxy.
	// Disable thinking/adaptive thinking because Kiro doesn't return thinking blocks,
	// which causes the Anthropic SDK to silently fail when it expects them.
	claudeCmd.Env = append(os.Environ(),
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_AUTH_TOKEN=claude2kiro",
		"CLAUDE2KIRO=1",
		"CLAUDE_CODE_DISABLE_THINKING=1",
	)
	claudeCmd.Stdin = os.Stdin
	claudeCmd.Stdout = os.Stdout
	claudeCmd.Stderr = os.Stderr

	// 6. Run claude (blocks until it exits)
	fmt.Printf("Launching: claude %s\n", strings.Join(claudeArgs, " "))
	runErr := claudeCmd.Run()

	// 7. Shutdown proxy gracefully
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)

	fmt.Println("Proxy stopped.")

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		os.Exit(1)
	}
}

// testProxy sends a test request to the Kiro backend and dumps the raw SSE response.
// Usage: claude2kiro test [message] [model]
// This is a debug tool to verify the proxy/backend is working correctly.
func testProxy() {
	message := "Say hello in one sentence."
	model := "claude-sonnet-4-6"
	if len(os.Args) > 2 {
		message = os.Args[2]
	}
	if len(os.Args) > 3 {
		model = os.Args[3]
	}

	fmt.Printf("=== claude2kiro test ===\n")
	fmt.Printf("Message: %s\n", message)
	fmt.Printf("Model:   %s -> %s\n", model, getKiroModelID(model))

	// Get token
	token, err := getToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: No token found: %v\nRun 'claude2kiro login' first.\n", err)
		os.Exit(1)
	}
	fmt.Printf("Token:   %s...%s (expires: %s)\n", token.AccessToken[:8], token.AccessToken[len(token.AccessToken)-4:], token.ExpiresAt)

	// Build request
	anthropicReq := AnthropicRequest{
		Model:     model,
		MaxTokens: 256,
		Stream:    true,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: message},
		},
	}

	cwReq := buildCodeWhispererRequest(anthropicReq, token)
	reqBody, err := json.Marshal(cwReq)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to marshal request: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n--- CodeWhisperer Request ---\n")
	// Pretty print (truncate if too long)
	prettyReq, _ := json.MarshalIndent(cwReq, "", "  ")
	reqStr := string(prettyReq)
	if len(reqStr) > 2000 {
		reqStr = reqStr[:2000] + "\n...(truncated)"
	}
	fmt.Println(reqStr)

	// Send to CodeWhisperer
	cfg := config.Get()
	endpoint := cfg.Advanced.CodeWhispererEndpoint
	fmt.Printf("\n--- Sending to %s ---\n", endpoint)

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to create HTTP request: %v\n", err)
		os.Exit(1)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Request failed: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	fmt.Printf("Status:  %d %s\n", resp.StatusCode, resp.Status)
	fmt.Printf("Headers:\n")
	for k, v := range resp.Header {
		fmt.Printf("  %s: %s\n", k, strings.Join(v, ", "))
	}

	// Read response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to read response: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n--- Raw Response (%d bytes) ---\n", len(respBody))

	if resp.StatusCode != http.StatusOK {
		// Try to show error as text
		fmt.Println(string(respBody))
		os.Exit(1)
	}

	// Save raw response for debugging
	debugDir, _ := os.UserHomeDir()
	debugPath := filepath.Join(debugDir, ".claude2kiro", "last-test-response.raw")
	os.MkdirAll(filepath.Dir(debugPath), 0755)
	os.WriteFile(debugPath, respBody, 0600)
	fmt.Printf("(saved raw to %s)\n", debugPath)

	// Parse events
	events := parser.ParseEvents(respBody)
	fmt.Printf("\n--- Parsed Events (%d) ---\n", len(events))

	fullText := ""
	for i, evt := range events {
		// SSEEvent has .Event (string) and .Data (interface{})
		dataJSON, _ := json.Marshal(evt.Data)
		dataStr := string(dataJSON)
		if len(dataStr) > 300 {
			dataStr = dataStr[:300] + "..."
		}
		fmt.Printf("[%d] event=%s data=%s\n", i, evt.Event, dataStr)

		// Extract text from content_block_delta events
		if evt.Event == "content_block_delta" {
			if dataMap, ok := evt.Data.(map[string]interface{}); ok {
				if delta, ok := dataMap["delta"].(map[string]interface{}); ok {
					if text, ok := delta["text"].(string); ok {
						fullText += text
					}
				}
			}
		}
		// Also try assistantResponseEvent format (raw Kiro events before conversion)
		if dataMap, ok := evt.Data.(map[string]interface{}); ok {
			if content, ok := dataMap["content"].(string); ok && content != "" {
				fullText += content
			}
		}
	}

	fmt.Printf("\n--- Extracted Text ---\n")
	if fullText == "" {
		fmt.Println("(empty - no text content found in events)")
	} else {
		fmt.Println(fullText)
	}
}

// printUsage displays CLI usage information
func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  claude2kiro                         - Launch interactive TUI")
	fmt.Println("  claude2kiro login [method]          - Login via browser (interactive menu)")
	fmt.Println("    Methods (optional - shows menu if omitted):")
	fmt.Println("      github                          - Login with GitHub")
	fmt.Println("      google                          - Login with Google")
	fmt.Println("      builderid                       - Login with AWS Builder ID")
	fmt.Println("      idc [start-url] [region]        - Login with Enterprise Identity Center")
	fmt.Println("    Tip: Just run 'claude2kiro login' for interactive selection")
	fmt.Println("")
	fmt.Println("  claude2kiro read           - Read and display token")
	fmt.Println("  claude2kiro refresh        - Refresh the access token")
	fmt.Println("  claude2kiro logout         - Clear saved credentials and token")
	fmt.Println("  claude2kiro export         - Export environment variables")
	fmt.Println("  claude2kiro run [args...]       - Start proxy + launch claude (per-session, no global config)")
	fmt.Println("  claude2kiro test [msg] [model]  - Send test request to Kiro backend (debug tool)")
	fmt.Println("  claude2kiro claude              - Configure Claude Code settings (global)")
	fmt.Println("  claude2kiro server [port]       - Start Anthropic API proxy server (headless)")
	fmt.Println("  claude2kiro migrate-logs [date] - Migrate log files to use attachment store")
	fmt.Println("                                    (date format: 2026-01-02, omit for all)")
	fmt.Println("")
	fmt.Println("  Author: https://github.com/sgeraldes/claude2kiro")
}

// runTUI launches the interactive TUI
func runTUI() {
	// Create TUI commands
	commands := tui.Commands{
		Login: cmd.LoginCmd,
		StartServer: func(port string, lg *logger.Logger) func() tea.Msg {
			return func() tea.Msg {
				// Start the server in a goroutine
				go startServerWithLogger(port, lg)
				return nil
			}
		},
		RefreshToken:    cmd.RefreshTokenCmd,
		ViewToken:       cmd.ViewTokenCmd,
		ExportEnv:       cmd.ExportEnvCmd,
		ConfigureClaude: cmd.ConfigureClaudeCmd,
		Unconfigure:     cmd.UnconfigureCmd,
		ViewCredits:     cmd.ViewCreditsCmd,
		Logout:          cmd.LogoutCmd,
		GetTokenExpiry:  cmd.GetTokenExpiry,
		HasToken:        cmd.HasToken,
		IsTokenExpired:  cmd.IsTokenExpired,
		TryRefreshToken: cmd.TryRefreshToken,
	}

	// Create root model
	model := tui.NewRootModel(commands)

	// Create program
	p := tea.NewProgram(model, tea.WithAltScreen())

	// Set program reference for logger
	model.SetProgram(p)

	// Run the program
	if _, err := p.Run(); err != nil {
		fmt.Printf("Error running TUI: %v\n", err)
		os.Exit(1)
	}
}


// extractSessionID extracts session identifiers from the request metadata
// The user_id format is like: "user_..._session_ce40736e-1347-467a-9cce-181e245edd92"
// Returns (shortID, fullUUID) where shortID is the last 8 chars for display
func extractSessionID(metadata map[string]any) (string, string) {
	if metadata == nil {
		return "", ""
	}
	userID, ok := metadata["user_id"].(string)
	if !ok || userID == "" {
		return "", ""
	}
	// Extract session UUID from the end of the user_id string
	// Format: user_..._session_<uuid>
	if idx := strings.LastIndex(userID, "_session_"); idx != -1 {
		fullUUID := userID[idx+9:] // Skip "_session_"
		// Return last 8 chars for display, plus full UUID
		if len(fullUUID) >= 8 {
			shortID := fullUUID[len(fullUUID)-8:]
			return shortID, fullUUID
		}
		return fullUUID, fullUUID
	}
	// Fallback: return last 8 chars of the whole user_id
	if len(userID) >= 8 {
		return userID[len(userID)-8:], ""
	}
	return userID, ""
}

// decompressResponse handles gzip/deflate decompression for non-streaming responses
func decompressResponse(resp *http.Response) ([]byte, error) {
	encoding := resp.Header.Get("Content-Encoding")
	var reader io.Reader = resp.Body

	switch encoding {
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip init failed: %w", err)
		}
		defer gzipReader.Close()
		reader = gzipReader
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
	case "deflate":
		flateReader := flate.NewReader(resp.Body)
		defer flateReader.Close()
		reader = flateReader
		resp.Header.Del("Content-Encoding")
		resp.Header.Del("Content-Length")
	case "":
		// No encoding
	default:
		return nil, fmt.Errorf("unsupported Content-Encoding: %s", encoding)
	}

	data, err := io.ReadAll(io.LimitReader(reader, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read/decompress failed: %w", err)
	}
	return data, nil
}

// AnthropicSSEEvent represents a parsed SSE event from Anthropic API
type AnthropicSSEEvent struct {
	Type string
	Data map[string]interface{}
}

// parseAnthropicSSE parses Anthropic SSE response into structured events
func parseAnthropicSSE(body []byte) []AnthropicSSEEvent {
	var events []AnthropicSSEEvent
	lines := strings.Split(string(body), "\n")
	var currentEvent, currentData string

	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			currentData = strings.TrimPrefix(line, "data: ")
		} else if line == "" && currentEvent != "" && currentData != "" {
			var data map[string]interface{}
			if json.Unmarshal([]byte(currentData), &data) == nil {
				events = append(events, AnthropicSSEEvent{Type: currentEvent, Data: data})
			}
			currentEvent, currentData = "", ""
		}
	}
	return events
}

// forwardToAnthropic forwards a request directly to Anthropic API as a TRUE bypass proxy
// extractAnthropicTextPreview extracts the first N characters of text from an Anthropic SSE response
func extractAnthropicTextPreview(sseResponse string, maxLen int) string {
	// Parse SSE format looking for content_block_delta events with text
	lines := strings.Split(sseResponse, "\n")
	var textParts []string

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		// Look for event: content_block_delta
		if strings.HasPrefix(line, "event: content_block_delta") {
			// Next line should be "data: {...}"
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				dataJSON := strings.TrimPrefix(lines[i+1], "data: ")
				var data map[string]interface{}
				if err := jsonStr.Unmarshal([]byte(dataJSON), &data); err == nil {
					if delta, ok := data["delta"].(map[string]interface{}); ok {
						if text, ok := delta["text"].(string); ok {
							textParts = append(textParts, text)
						}
					}
				}
			}
		}
	}

	fullText := strings.Join(textParts, "")
	if len(fullText) > maxLen {
		return fullText[:maxLen] + "..."
	}
	if fullText == "" {
		return "[no text content]"
	}
	return fullText
}
// Copies ALL headers from the original request unchanged
func forwardToAnthropic(originalReq *http.Request, body []byte, lg *logger.Logger) (*http.Response, error) {
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Copy ALL headers from original request (true bypass proxy)
	for key, values := range originalReq.Header {
		// Skip hop-by-hop headers and headers that must not be forwarded
		switch key {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailers", "Transfer-Encoding", "Upgrade",
			"Host", "Content-Length": // Host must be target, Content-Length set by Go
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// No timeout for streaming requests - SSE streams can run for minutes
	client := &http.Client{}
	return client.Do(req)
}

// forwardToAnthropicWithHeaders forwards a request to Anthropic API using pre-copied headers
// Used by comparison mode goroutines to avoid race conditions with request reuse
func forwardToAnthropicWithHeaders(headers http.Header, body []byte, lg *logger.Logger) (*http.Response, error) {
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Copy headers (already filtered by caller)
	for key, values := range headers {
		// Skip hop-by-hop headers and headers that must not be forwarded
		switch key {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailers", "Transfer-Encoding", "Upgrade",
			"Host", "Content-Length":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// No timeout for streaming requests - SSE streams can run for minutes
	client := &http.Client{}
	return client.Do(req)
}

// forwardToAnthropicHeadless forwards a request to Anthropic API as a TRUE bypass proxy (headless/CLI version)
// Copies ALL headers from the original request unchanged
func forwardToAnthropicHeadless(originalReq *http.Request, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Copy ALL headers from original request (true bypass proxy)
	for key, values := range originalReq.Header {
		// Skip hop-by-hop headers and headers that must not be forwarded
		switch key {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailers", "Transfer-Encoding", "Upgrade",
			"Host", "Content-Length": // Host must be target, Content-Length set by Go
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// No timeout for streaming requests - SSE streams can run for minutes
	client := &http.Client{}
	return client.Do(req)
}

// forwardToAnthropicHeadlessWithHeaders forwards a request to Anthropic API using pre-copied headers (CLI version)
// Used by comparison mode goroutines to avoid race conditions with request reuse
func forwardToAnthropicHeadlessWithHeaders(headers http.Header, body []byte) (*http.Response, error) {
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	// Copy headers (already filtered by caller)
	for key, values := range headers {
		// Skip hop-by-hop headers and headers that must not be forwarded
		switch key {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailers", "Transfer-Encoding", "Upgrade",
			"Host", "Content-Length":
			continue
		}
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}

	// No timeout for streaming requests - SSE streams can run for minutes
	client := &http.Client{}
	return client.Do(req)
}

// startServerWithLogger starts the HTTP proxy server with TUI logging
func startServerWithLogger(port string, lg *logger.Logger) {
	// Create router
	mux := http.NewServeMux()

	// Register all endpoints
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		// Only handle POST requests
		if r.Method != http.MethodPost {
			lg.LogError("Unsupported request method: " + r.Method)
			http.Error(w, "Only POST requests are supported", http.StatusMethodNotAllowed)
			return
		}

		// Get current token, refreshing proactively if expired/expiring soon
		token, err := getToken()
		if err != nil {
			lg.LogError(fmt.Sprintf("Failed to get token: %v", err))
			http.Error(w, fmt.Sprintf("Failed to get token: %v", err), http.StatusInternalServerError)
			return
		}

		// Proactive refresh: if token expires within 5 minutes, refresh it first
		if token.ExpiresAt != "" {
			if expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt); err == nil {
				if time.Until(expiresAt) < 5*time.Minute {
					lg.LogInfo("Token expiring soon, refreshing proactively...")
					if err := tryRefreshToken(); err != nil {
						lg.LogError(fmt.Sprintf("Proactive token refresh failed: %v", err))
						// Continue with current token, let 403 handler deal with it
					} else {
						lg.LogInfo("Token refreshed successfully")
						// Re-read the refreshed token
						token, err = getToken()
						if err != nil {
							lg.LogError(fmt.Sprintf("Failed to get refreshed token: %v", err))
							http.Error(w, fmt.Sprintf("Failed to get refreshed token: %v", err), http.StatusInternalServerError)
							return
						}
					}
				}
			}
		}

		// Read request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			lg.LogError(fmt.Sprintf("Failed to read request body: %v", err))
			http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		// Parse Anthropic request
		var anthropicReq AnthropicRequest
		if err := jsonStr.Unmarshal(body, &anthropicReq); err != nil {
			lg.LogError(fmt.Sprintf("Failed to parse request body: %v", err))
			http.Error(w, fmt.Sprintf("Failed to parse request body: %v", err), http.StatusBadRequest)
			return
		}

		// Extract session ID from metadata for tracking
		sessionID, fullUUID := extractSessionID(anthropicReq.Metadata)

		// Calculate parse duration (time from request start to now)
		parseDuration := time.Since(startTime)

		// Log the request and get request ID/seqNum for correlation
		// Status 0 = OK (request parsed successfully)
		reqResult := lg.LogRequest(anthropicReq.Model, r.Method, r.URL.Path, string(body), sessionID, fullUUID, 0, parseDuration)

		// Basic validation
		if anthropicReq.Model == "" {
			http.Error(w, `{"message":"Missing required field: model"}`, http.StatusBadRequest)
			return
		}
		if len(anthropicReq.Messages) == 0 {
			http.Error(w, `{"message":"Missing required field: messages"}`, http.StatusBadRequest)
			return
		}

		// Check for Anthropic bypass/comparison modes
		cfg := config.Get()

		if cfg.Advanced.AnthropicDirect {
			// Anthropic Direct Mode: forward request unchanged to Anthropic
			resp, err := forwardToAnthropic(r, body, lg)
			if err != nil {
				lg.LogError(fmt.Sprintf("[ANTHROPIC DIRECT] Forward failed: %v", err))
				http.Error(w, fmt.Sprintf("Anthropic forward failed: %v", err), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			// Copy ALL response headers
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)

			// Check if this is a streaming response (SSE)
			if resp.Header.Get("Content-Type") == "text/event-stream" {
				// Read full response body to parse SSE events
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					lg.LogError(fmt.Sprintf("[ANTHROPIC DIRECT] Failed to read response: %v", err))
					return
				}

				// Parse SSE events and extract text content for preview
				events := parseAnthropicSSE(respBody)
				var textParts []string
				for _, evt := range events {
					if evt.Type == "content_block_delta" {
						if delta, ok := evt.Data["delta"].(map[string]interface{}); ok {
							if text, ok := delta["text"].(string); ok {
								textParts = append(textParts, text)
							}
						}
					}
				}

				// Forward events to client
				flusher, ok := w.(http.Flusher)
				if ok {
					w.Write(respBody)
					flusher.Flush()
				} else {
					w.Write(respBody)
				}

				// Log with parsed text preview and full SSE body
				preview := strings.Join(textParts, "")
				if len(preview) > 80 {
					preview = preview[:80] + "..."
				}
				if preview == "" {
					preview = "[no text content]"
				}
				lg.LogResponseWithBody(resp.StatusCode, r.URL.Path, time.Since(startTime), preview, string(respBody), sessionID, fullUUID, reqResult.RequestID, reqResult.SeqNum)
			} else {
				// Non-streaming: decompress and read entire body
				respBody, err := decompressResponse(resp)
				if err != nil {
					lg.LogError(fmt.Sprintf("[ANTHROPIC DIRECT] Decompression failed: %v", err))
					http.Error(w, "Failed to decompress response", http.StatusInternalServerError)
					return
				}
				w.Write(respBody)

				// Log response with preview and full body
				duration := time.Since(startTime)
				preview := string(respBody)
				if len(preview) > 200 {
					preview = preview[:200] + "..."
				}
				lg.LogResponseWithBody(resp.StatusCode, r.URL.Path, duration, preview, string(respBody), sessionID, fullUUID, reqResult.RequestID, reqResult.SeqNum)
			}
			return
		}

		// Comparison mode: shared timestamp for correlated saves
		var comparisonTimestamp string

		if cfg.Advanced.ComparisonMode {
			// Generate shared timestamp for both Anthropic and Kiro saves
			comparisonTimestamp = time.Now().Format("20060102-150405")

			// Comparison Mode: send to both Anthropic and Kiro in parallel
			// Copy headers before goroutine to avoid race condition (request may be reused)
			headersCopy := make(http.Header)
			for k, v := range r.Header {
				headersCopy[k] = v
			}
			// Capture IDs for correlation
			compSessionID := sessionID
			compRequestID := reqResult.RequestID
			// Anthropic request runs in background goroutine
			go func(headers http.Header, sid, rid, ts string) {
				lg.LogComparison(sid, rid, "Sending parallel request to Anthropic")
				resp, err := forwardToAnthropicWithHeaders(headers, body, lg)
				if err != nil {
					lg.LogComparison(sid, rid, fmt.Sprintf("Anthropic error: %v", err))
					return
				}
				defer resp.Body.Close()
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					lg.LogComparison(sid, rid, fmt.Sprintf("Failed to read Anthropic response: %v", err))
					return
				}

				// Extract text preview from SSE response
				preview := extractAnthropicTextPreview(string(respBody), 80)

				// Save to secure debug file with request ID in filename
				debugFile, err := debug.WriteDebugFileWithScrub(fmt.Sprintf("comparison-%s-anthropic", rid), respBody)
				if err != nil {
					lg.LogComparison(sid, rid, fmt.Sprintf("Failed to save debug file: %v", err))
					return
				}
				lg.LogComparison(sid, rid, fmt.Sprintf("Anthropic: preview=%q (%d bytes) -> %s", preview, len(respBody), debugFile))
			}(headersCopy, compSessionID, compRequestID, comparisonTimestamp)
			// Continue with normal Kiro flow below...
		}

		// Handle streaming request
		var responsePreview string
		var capturedKiroEvents []CapturedSSEEvent
		if anthropicReq.Stream {
			// Pass capture slice if in comparison mode
			var capture *[]CapturedSSEEvent
			if cfg.Advanced.ComparisonMode {
				capture = &capturedKiroEvents
			}
			responsePreview = handleStreamRequestWithLogger(w, anthropicReq, token, lg, sessionID, reqResult.RequestID, capture)
		} else {
			handleNonStreamRequest(w, anthropicReq, token)
		}

		// Log response
		duration := time.Since(startTime)
		lg.LogResponse(http.StatusOK, r.URL.Path, duration, responsePreview, sessionID, fullUUID, reqResult.RequestID, reqResult.SeqNum)

		// Save Kiro events if in comparison mode
		if cfg.Advanced.ComparisonMode && len(capturedKiroEvents) > 0 {
			// Format events as SSE text
			var sseBuffer strings.Builder
			for _, event := range capturedKiroEvents {
				sseBuffer.WriteString(fmt.Sprintf("event: %s\n", event.Event))
				sseBuffer.WriteString(fmt.Sprintf("data: %s\n\n", event.Data))
			}

			debugFile, err := debug.WriteDebugFile(fmt.Sprintf("comparison-%s-kiro", reqResult.RequestID), []byte(sseBuffer.String()))
			if err != nil {
				lg.LogComparison(sessionID, reqResult.RequestID, fmt.Sprintf("Failed to save Kiro events: %v", err))
			} else {
				lg.LogComparison(sessionID, reqResult.RequestID, fmt.Sprintf("Kiro: %d events (%d bytes) -> %s", len(capturedKiroEvents), sseBuffer.Len(), debugFile))
			}
		}
	})

	// Add health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// Add 404 handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		lg.LogInfo("Unknown endpoint accessed: " + r.URL.Path)
		http.Error(w, "404 Not Found", http.StatusNotFound)
	})

	// Log server start
	lg.LogInfo(fmt.Sprintf("Server started on port %s", port))

	// Notify TUI that server started
	if p := lg.GetProgram(); p != nil {
		p.Send(dashboard.ServerStartedMsg{Port: port})
	}

	// Start server
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		lg.LogError(fmt.Sprintf("Server error: %v", err))
	}
}

// handleStreamRequestWithLogger is like handleStreamRequest but with TUI logging
// capturedEvents: optional pointer to slice for capturing SSE events (for comparison mode)
func handleStreamRequestWithLogger(w http.ResponseWriter, anthropicReq AnthropicRequest, token TokenData, lg *logger.Logger, sessionID, requestID string, capturedEvents *[]CapturedSSEEvent) string {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return ""
	}

	messageId := fmt.Sprintf("msg_%s", time.Now().Format("20060102150405"))

	// Build CodeWhisperer request
	cwReq := buildCodeWhispererRequest(anthropicReq, token)

	// Serialize request body
	cwReqBody, err := jsonStr.Marshal(cwReq)
	if err != nil {
		sendErrorEvent(w, flusher, "Failed to serialize request", err)
		return ""
	}

	// Debug: Write request to secure debug directory (with sensitive data scrubbed)
	debug.WriteDebugFileWithScrub("cw-request", cwReqBody)

	// Log the conversationId for tracing
	lg.LogInfo(fmt.Sprintf("Request convId=%s model=%s historyLen=%d", cwReq.ConversationState.ConversationId[:8], anthropicReq.Model, len(cwReq.ConversationState.History)))

	// Create streaming request
	cfg := config.Get()
	proxyReq, err := http.NewRequest(
		http.MethodPost,
		cfg.Advanced.CodeWhispererEndpoint,
		bytes.NewBuffer(cwReqBody),
	)
	if err != nil {
		sendErrorEvent(w, flusher, "Failed to create proxy request", err)
		return ""
	}

	// Set request headers
	proxyReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	proxyReq.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	// Acquire semaphore to limit concurrent Kiro requests
	kiroRequestSema <- struct{}{}
	defer func() { <-kiroRequestSema }()

	// Send request with retry logic for transient errors
	client := &http.Client{}
	var resp *http.Response
	var lastErr error

	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			// Recreate request for retry (body was consumed)
			proxyReq, err = http.NewRequest(
				http.MethodPost,
				cfg.Advanced.CodeWhispererEndpoint,
				bytes.NewBuffer(cwReqBody),
			)
			if err != nil {
				sendErrorEvent(w, flusher, "Failed to create retry request", err)
				return ""
			}
			proxyReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
			proxyReq.Header.Set("Content-Type", "application/json")
			proxyReq.Header.Set("Accept", "text/event-stream")
			proxyReq.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))
			time.Sleep(100 * time.Millisecond) // Brief delay before retry
		}

		resp, lastErr = client.Do(proxyReq)
		if lastErr != nil {
			lg.LogError(fmt.Sprintf("CodeWhisperer request error (attempt %d): %v", attempt+1, lastErr))
			continue
		}

		if resp.StatusCode == http.StatusOK {
			break // Success
		}

		// Read error body
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 400 && attempt < 2 {
			// Transient 400 error - retry
			lg.LogInfo(fmt.Sprintf("400 error convId=%s attempt=%d reqSize=%d: %s", cwReq.ConversationState.ConversationId[:8], attempt+1, len(cwReqBody), string(body)))
			continue
		}

		// Non-retryable error or final attempt - save the failing request for debugging
		debug.WriteDebugFileWithScrub("cw-FAILED", cwReqBody)
		lg.LogError(fmt.Sprintf("FINAL ERROR convId=%s status=%d: %s", cwReq.ConversationState.ConversationId[:8], resp.StatusCode, string(body)))

		if resp.StatusCode == 403 {
			if err := tryRefreshToken(); err != nil {
				lg.LogError(fmt.Sprintf("Token refresh failed: %v", err))
				sendErrorEvent(w, flusher, "error", fmt.Errorf("Token expired, refresh failed: %v. Please re-login", err))
			} else {
				lg.LogInfo("Token refreshed successfully")
				sendErrorEvent(w, flusher, "error", fmt.Errorf("Token refreshed, please retry"))
			}
		} else {
			sendErrorEvent(w, flusher, "error", fmt.Errorf("CodeWhisperer Error: %s", string(body)))
		}
		return ""
	}

	if lastErr != nil {
		sendErrorEvent(w, flusher, "CodeWhisperer request error after retries", lastErr)
		return ""
	}
	defer resp.Body.Close()

	// Read entire response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		sendErrorEvent(w, flusher, "error", fmt.Errorf("CodeWhisperer Error: failed to read response"))
		return ""
	}

	// Debug: Save raw response for analysis
	debug.WriteDebugFile("cw-response", respBody)

	// Use CodeWhisperer parser
	events := parser.ParseEvents(respBody)
	var responseText strings.Builder

	// Debug: Log if no events parsed
	if len(events) == 0 {
		lg.LogError(fmt.Sprintf("WARNING: Parser returned 0 events from %d bytes response", len(respBody)))
	}

	// Use new SSE builder if feature flag is enabled
	if cfg.Advanced.UseNewSSEBuilder {
		return handleStreamRequestWithLoggerNewBuilder(w, flusher, events, anthropicReq, messageId, cfg, lg, sessionID, requestID, capturedEvents, len(respBody))
	}

	// --- Old code path (fallback) ---
	if len(events) > 0 {
		// Check if this is a tool-only response (no text content events)
		hasTextContent := false
		hasToolUse := false
		parserSentMessageDelta := false
		toolBlockCount := 0
		for _, e := range events {
			if e.Event == "content_block_delta" {
				if dataMap, ok := e.Data.(map[string]any); ok {
					if delta, ok := dataMap["delta"].(map[string]any); ok {
						if _, ok := delta["text"]; ok {
							hasTextContent = true
						}
						if _, ok := delta["partial_json"]; ok {
							hasToolUse = true
						}
					}
				}
			}
			if e.Event == "content_block_start" {
				if dataMap, ok := e.Data.(map[string]any); ok {
					if cb, ok := dataMap["content_block"].(map[string]any); ok {
						if cbType, ok := cb["type"].(string); ok && cbType == "tool_use" {
							hasToolUse = true
							toolBlockCount++
						}
					}
				}
			}
			if e.Event == "message_delta" {
				// Parser already sent a message_delta (for tool_use stop)
				parserSentMessageDelta = true
			}
		}

		// Log event analysis results
		if cfg.Advanced.ComparisonMode {
			// In comparison mode, show preview of actual response text
			textPreview := responseText.String()
			if len(textPreview) > 80 {
				textPreview = textPreview[:80]
			}
			lg.LogComparison(sessionID, requestID, fmt.Sprintf("Kiro: %d events, preview=%q (%d bytes), tools=%d",
				len(events), textPreview, len(respBody), toolBlockCount))
		} else {
			lg.LogInfo(fmt.Sprintf("Parser: %d events, hasText=%v, hasToolUse=%v (tools=%d), parserSentDelta=%v",
				len(events), hasTextContent, hasToolUse, toolBlockCount, parserSentMessageDelta))
		}

		// Send start event
		messageStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            messageId,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         anthropicReq.Model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  len(getMessageContent(anthropicReq.Messages[0].Content)),
					"output_tokens": 1,
				},
			},
		}
		sendSSEEvent(w, flusher, "message_start", messageStart, capturedEvents)
		sendSSEEvent(w, flusher, "ping", map[string]string{"type": "ping"}, capturedEvents)

		// Only send text content_block_start if there's text content (not tool-only)
		if hasTextContent || !hasToolUse {
			contentBlockStart := map[string]any{
				"content_block": map[string]any{"text": "", "type": "text"},
				"index":         0,
				"type":          "content_block_start",
			}
			sendSSEEvent(w, flusher, "content_block_start", contentBlockStart, capturedEvents)
		}

		outputTokens := 0
		for _, e := range events {
			sendSSEEvent(w, flusher, e.Event, e.Data, capturedEvents)

			if e.Event == "content_block_delta" {
				outputTokens = len(getMessageContent(e.Data))
				if dataMap, ok := e.Data.(map[string]any); ok {
					if delta, ok := dataMap["delta"].(map[string]any); ok {
						if text, ok := delta["text"].(string); ok {
							responseText.WriteString(text)
						}
					}
				}
			}

			// Random delay for natural streaming
			time.Sleep(time.Duration(mathrand.Intn(int(cfg.Network.StreamingDelayMax.Milliseconds()))) * time.Millisecond)
		}

		// Only send text block stop if we sent text block start
		if hasTextContent || !hasToolUse {
			contentBlockStop := map[string]any{"index": 0, "type": "content_block_stop"}
			sendSSEEvent(w, flusher, "content_block_stop", contentBlockStop, capturedEvents)
		}

		// Always send message_delta if parser didn't already send one
		if !parserSentMessageDelta {
			stopReason := "end_turn"
			if hasToolUse && !hasTextContent {
				stopReason = "tool_use"
			}
			contentBlockStopReason := map[string]any{
				"type":  "message_delta",
				"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
				"usage": map[string]any{"output_tokens": outputTokens},
			}
			sendSSEEvent(w, flusher, "message_delta", contentBlockStopReason, capturedEvents)
		}

		messageStop := map[string]any{"type": "message_stop"}
		sendSSEEvent(w, flusher, "message_stop", messageStop, capturedEvents)
	} else {
		// No events parsed - send an empty response to prevent Claude Code from hanging
		lg.LogError("Sending empty response due to no parsed events")
		messageStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            messageId,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         anthropicReq.Model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage":         map[string]any{"input_tokens": 0, "output_tokens": 0},
			},
		}
		sendSSEEvent(w, flusher, "message_start", messageStart, capturedEvents)
		contentBlockStart := map[string]any{
			"content_block": map[string]any{"text": "[Error: No response received from backend]", "type": "text"},
			"index":         0,
			"type":          "content_block_start",
		}
		sendSSEEvent(w, flusher, "content_block_start", contentBlockStart, capturedEvents)
		sendSSEEvent(w, flusher, "content_block_stop", map[string]any{"index": 0, "type": "content_block_stop"}, capturedEvents)
		sendSSEEvent(w, flusher, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 0},
		}, capturedEvents)
		sendSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"}, capturedEvents)
		responseText.WriteString("[Error: No response received from backend]")
	}

	result := responseText.String()

	// Validate response - warn on suspiciously short/malformed responses
	if len(result) > 0 && len(result) < 10 {
		trimmed := strings.TrimSpace(result)
		// Check for responses that look like truncated JSON/code
		if trimmed == "{" || trimmed == "[" || trimmed == "```" ||
			strings.HasPrefix(trimmed, "{") && !strings.HasSuffix(trimmed, "}") {
			lg.LogError(fmt.Sprintf("WARNING: Suspiciously short/truncated response detected: %q", result))
		}
	}

	return result
}

// capturedSSEEventCapture wraps CapturedSSEEvent slice to implement sse.EventCapture interface
type capturedSSEEventCapture struct {
	events *[]CapturedSSEEvent
}

func (c *capturedSSEEventCapture) Append(event, data string) {
	if c.events != nil {
		*c.events = append(*c.events, CapturedSSEEvent{Event: event, Data: data})
	}
}

// handleStreamRequestWithLoggerNewBuilder handles SSE streaming using the new sse.StreamWriter.
// This is used when config.Advanced.UseNewSSEBuilder is true.
func handleStreamRequestWithLoggerNewBuilder(w http.ResponseWriter, flusher http.Flusher, events []parser.SSEEvent, anthropicReq AnthropicRequest, messageId string, cfg *config.Config, lg *logger.Logger, sessionID, requestID string, capturedEvents *[]CapturedSSEEvent, respBodyLen int) string {
	streamCfg := sse.StreamConfig{
		MessageID:         messageId,
		Model:             anthropicReq.Model,
		InputTokens:       len(getMessageContent(anthropicReq.Messages[0].Content)),
		StreamingDelayMax: cfg.Network.StreamingDelayMax,
	}

	delayFn := func() {
		time.Sleep(time.Duration(mathrand.Intn(int(cfg.Network.StreamingDelayMax.Milliseconds()))) * time.Millisecond)
	}

	// Analyze events for logging
	analysis := sse.AnalyzeEvents(events)

	// Log event analysis results
	if cfg.Advanced.ComparisonMode {
		lg.LogComparison(sessionID, requestID, fmt.Sprintf("Kiro: %d events (%d bytes), tools=%d",
			len(events), respBodyLen, analysis.ToolBlockCount))
	} else {
		lg.LogInfo(fmt.Sprintf("Parser: %d events, hasText=%v, hasToolUse=%v (tools=%d), parserSentDelta=%v",
			len(events), analysis.HasTextContent, analysis.HasToolUse, analysis.ToolBlockCount, analysis.ParserSentMessageDelta))
	}

	// Create capture wrapper if needed
	var capture sse.EventCapture
	if capturedEvents != nil {
		capture = &capturedSSEEventCapture{events: capturedEvents}
	}

	var result string
	if len(events) > 0 {
		result = sse.StreamEventsWithCapture(w, flusher, events, streamCfg, capture, delayFn)
	} else {
		lg.LogError("Sending empty response due to no parsed events")
		result = sse.StreamEmptyResponseWithCapture(w, flusher, streamCfg, capture, "[Error: No response received from backend]")
	}

	// Validate response - warn on suspiciously short/malformed responses
	if len(result) > 0 && len(result) < 10 {
		trimmed := strings.TrimSpace(result)
		if trimmed == "{" || trimmed == "[" || trimmed == "```" ||
			strings.HasPrefix(trimmed, "{") && !strings.HasSuffix(trimmed, "}") {
			lg.LogError(fmt.Sprintf("WARNING: Suspiciously short/truncated response detected: %q", result))
		}
	}

	return result
}

// getTokenFilePath returns the cross-platform token file path
func getTokenFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Failed to get user home directory: %v\n", err)
		os.Exit(1)
	}

	return filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
}

// getLoginConfigPath returns the path for login config file
func getLoginConfigPath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".aws", "sso", "cache", "claude2kiro-login-config.json")
}

// readLoginConfig reads the saved login configuration
func readLoginConfig() (*LoginConfig, error) {
	path := getLoginConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config LoginConfig
	if err := jsonStr.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return &config, nil
}

// saveLoginConfig saves the login configuration for future use
func saveLoginConfig(config *LoginConfig) error {
	path := getLoginConfigPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := jsonStr.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// logout deletes saved login config and tokens
func logout() {
	configPath := getLoginConfigPath()
	tokenPath := getTokenFilePath()

	configDeleted := false
	tokenDeleted := false

	// Delete login config
	if err := os.Remove(configPath); err == nil {
		configDeleted = true
	}

	// Delete token file
	if err := os.Remove(tokenPath); err == nil {
		tokenDeleted = true
	}

	if configDeleted || tokenDeleted {
		fmt.Println("Logged out successfully.")
		if configDeleted {
			fmt.Println("  - Deleted login config")
		}
		if tokenDeleted {
			fmt.Println("  - Deleted auth token")
		}
	} else {
		fmt.Println("Already logged out (no saved credentials found).")
	}
}

// hasValidToken checks if there's a valid (non-expired) token
func hasValidToken() bool {
	tokenPath := getTokenFilePath()
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return false
	}

	var token TokenData
	if err := json.Unmarshal(data, &token); err != nil {
		return false
	}

	// Check if token exists and has required fields
	if token.AccessToken == "" {
		return false
	}

	// Check expiration if present
	if token.ExpiresAt != "" {
		expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
		if err == nil && time.Now().After(expiresAt) {
			return false // Token expired
		}
	}

	return true
}

// LoginMethod represents a login option for the interactive menu
type LoginMethod struct {
	Name string
	Desc string
}

// normalizeStartUrl converts short input to full URL
// Examples: "d5" -> "https://d5.awsapps.com/start"
//
//	"my-company" -> "https://my-company.awsapps.com/start"
func normalizeStartUrl(input string) string {
	input = strings.TrimSpace(input)
	if strings.HasPrefix(input, "https://") {
		return input
	}
	// Remove any partial suffixes the user might have typed
	input = strings.TrimSuffix(input, ".awsapps.com/start")
	input = strings.TrimSuffix(input, ".awsapps.com")
	input = strings.TrimSuffix(input, "/start")
	return fmt.Sprintf("https://%s.awsapps.com/start", input)
}

// getAWSRegions returns available AWS regions for SSO
func getAWSRegions() []string {
	return []string{
		"us-east-1",      // N. Virginia (default)
		"us-east-2",      // Ohio
		"us-west-1",      // N. California
		"us-west-2",      // Oregon
		"eu-west-1",      // Ireland
		"eu-west-2",      // London
		"eu-west-3",      // Paris
		"eu-central-1",   // Frankfurt
		"eu-north-1",     // Stockholm
		"ap-southeast-1", // Singapore
		"ap-southeast-2", // Sydney
		"ap-northeast-1", // Tokyo
		"ap-northeast-2", // Seoul
		"ap-south-1",     // Mumbai
		"sa-east-1",      // Sao Paulo
		"ca-central-1",   // Canada
	}
}

// interactiveLogin presents an interactive menu for first-time login
func interactiveLogin() *LoginConfig {
	methods := []LoginMethod{
		{"Github", "Social login via Github"},
		{"Google", "Social login via Google"},
		{"AWS Builder ID", "Free AWS developer account"},
		{"Enterprise Identity Center", "Organization SSO (requires start URL)"},
	}

	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   "\U0001F449 {{ .Name | cyan }} - {{ .Desc }}",
		Inactive: "   {{ .Name | white }} - {{ .Desc | faint }}",
		Selected: "\u2714 {{ .Name | green }}",
	}

	prompt := promptui.Select{
		Label:     "Select login method",
		Items:     methods,
		Templates: templates,
		Size:      4,
	}

	idx, _, err := prompt.Run()
	if err != nil {
		fmt.Printf("Login cancelled: %v\n", err)
		os.Exit(1)
	}

	// Map selection to config
	switch idx {
	case 0: // GitHub
		return &LoginConfig{Method: "github"}
	case 1: // Google
		return &LoginConfig{Method: "google"}
	case 2: // AWS Builder ID
		return &LoginConfig{Method: "builderid"}
	case 3: // Enterprise Identity Center
		return interactiveIdCLogin()
	}

	return &LoginConfig{Method: "github"}
}

// interactiveIdCLogin handles the IdC-specific prompts
func interactiveIdCLogin() *LoginConfig {
	// URL prompt with smart completion
	urlPrompt := promptui.Prompt{
		Label: "Start URL (e.g., 'd5' or 'https://d5.awsapps.com/start')",
		Validate: func(input string) error {
			if strings.TrimSpace(input) == "" {
				return fmt.Errorf("start URL is required")
			}
			return nil
		},
	}

	urlInput, err := urlPrompt.Run()
	if err != nil {
		fmt.Printf("Login cancelled: %v\n", err)
		os.Exit(1)
	}

	startUrl := normalizeStartUrl(urlInput)
	fmt.Printf("Using URL: %s\n", startUrl)

	// Region selection with search
	regions := getAWSRegions()

	regionPrompt := promptui.Select{
		Label: "AWS Region",
		Items: regions,
		Size:  8,
		Searcher: func(input string, idx int) bool {
			region := regions[idx]
			input = strings.ToLower(input)
			return strings.Contains(region, input)
		},
		StartInSearchMode: true,
	}

	_, region, err := regionPrompt.Run()
	if err != nil {
		fmt.Printf("Login cancelled: %v\n", err)
		os.Exit(1)
	}

	return &LoginConfig{
		Method:   "idc",
		StartUrl: startUrl,
		Region:   region,
	}
}

const (
	kiroAuthEndpoint = "https://prod.us-east-1.auth.desktop.kiro.dev"
	kiroVersion      = "0.11.28" // Kiro IDE version to impersonate
)

// generatePKCE generates PKCE code verifier and challenge
func generatePKCE() (verifier string, challenge string, err error) {
	// Generate 32 random bytes for code verifier
	verifierBytes := make([]byte, 32)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", fmt.Errorf("failed to generate random bytes: %v", err)
	}

	// Base64URL encode the verifier
	verifier = base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Generate challenge as SHA256 hash of verifier, base64url encoded
	hash := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(hash[:])

	return verifier, challenge, nil
}

// generateState generates a random state parameter for CSRF protection
func generateState() (string, error) {
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", fmt.Errorf("failed to generate state: %v", err)
	}
	return fmt.Sprintf("%x", stateBytes), nil
}

// openBrowser opens the specified URL in the default browser
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default: // Linux and others
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

// loginSocial performs browser-based OAuth login with GitHub or Google
func loginSocial(provider string) {
	fmt.Printf("Starting login with %s...\n", provider)

	// Generate PKCE values
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		fmt.Printf("Failed to generate PKCE: %v\n", err)
		os.Exit(1)
	}

	// Generate state for CSRF protection
	state, err := generateState()
	if err != nil {
		fmt.Printf("Failed to generate state: %v\n", err)
		os.Exit(1)
	}

	// Find available port and start local callback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("Failed to start callback server: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectUri := fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", port)

	fmt.Printf("Callback server started on port %d\n", port)

	// Channel to receive the auth code
	authCodeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Create HTTP server for callback with local mux (avoid polluting DefaultServeMux)
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}
	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		// Validate state
		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			errChan <- fmt.Errorf("state mismatch: expected %s, got %s", state, receivedState)
			http.Error(w, "Invalid state parameter", http.StatusBadRequest)
			return
		}

		// Check for error
		if errParam := r.URL.Query().Get("error"); errParam != "" {
			errDesc := r.URL.Query().Get("error_description")
			errChan <- fmt.Errorf("OAuth error: %s - %s", errParam, errDesc)
			http.Error(w, fmt.Sprintf("Authentication failed: %s", errDesc), http.StatusBadRequest)
			return
		}

		// Get auth code
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no authorization code received")
			http.Error(w, "No authorization code received", http.StatusBadRequest)
			return
		}

		// Send success response to browser
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`
			<!DOCTYPE html>
			<html>
			<head>
				<meta charset="UTF-8">
				<title>Login Successful</title>
				<style>
					body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
						   display: flex; justify-content: center; align-items: center;
						   height: 100vh; margin: 0; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); }
					.container { text-align: center; background: white; padding: 40px 60px;
								 border-radius: 16px; box-shadow: 0 10px 40px rgba(0,0,0,0.2); }
					h1 { color: #333; margin-bottom: 10px; }
					p { color: #666; }
				</style>
			</head>
			<body>
				<div class="container">
					<h1>✓ Login Successful!</h1>
					<p>You can close this window and return to the terminal.</p>
				</div>
			</body>
			</html>
		`))

		authCodeChan <- code
	})

	// Start server in goroutine
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			errChan <- fmt.Errorf("callback server error: %v", err)
		}
	}()

	// Build login URL
	loginUrl := fmt.Sprintf("%s/login?idp=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&state=%s",
		kiroAuthEndpoint,
		provider,
		url.QueryEscape(redirectUri),
		codeChallenge,
		state,
	)

	fmt.Println("Opening browser for authentication...")
	fmt.Printf("If the browser doesn't open, please visit:\n%s\n\n", loginUrl)

	if err := openBrowser(loginUrl); err != nil {
		fmt.Printf("Failed to open browser: %v\n", err)
		fmt.Println("Please open the URL above manually.")
	}

	fmt.Println("Waiting for authentication...")

	// Wait for auth code or error with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	select {
	case code := <-authCodeChan:
		// Shutdown server
		server.Shutdown(context.Background())

		fmt.Println("Authorization code received, exchanging for tokens...")

		// Exchange code for tokens
		token, err := exchangeCodeForTokens(code, codeVerifier, redirectUri, provider)
		if err != nil {
			fmt.Printf("Failed to exchange code for tokens: %v\n", err)
			os.Exit(1)
		}

		// Save token to file
		if err := saveToken(token); err != nil {
			fmt.Printf("Failed to save token: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("\n✓ Login successful!")
		fmt.Printf("Provider: %s\n", provider)
		fmt.Printf("Token expires at: %s\n", token.ExpiresAt)
		fmt.Println("\nYou can now run 'claude2kiro server' to start the proxy.")

	case err := <-errChan:
		server.Shutdown(context.Background())
		fmt.Printf("Authentication failed: %v\n", err)
		os.Exit(1)

	case <-ctx.Done():
		server.Shutdown(context.Background())
		fmt.Println("Authentication timed out. Please try again.")
		os.Exit(1)
	}
}

// IdC (Identity Center) authentication constants and functions

var idcScopes = []string{
	"codewhisperer:completions",
	"codewhisperer:analysis",
	"codewhisperer:conversations",
	"codewhisperer:transformations",
	"codewhisperer:taskassist",
}

// getClientIdHash generates a hash for the client ID based on start URL
func getClientIdHash(startUrl string) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf(`{"startUrl":"%s"}`, startUrl)))
	return fmt.Sprintf("%x", hash[:20]) // SHA1-like length
}

// getClientRegistrationPath returns the path for cached client registration
func getClientRegistrationPath(clientIdHash string) string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".aws", "sso", "cache", clientIdHash+".json")
}

// readClientRegistration reads cached client registration
func readClientRegistration(clientIdHash string) (*SSOClientRegistration, error) {
	path := getClientRegistrationPath(clientIdHash)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var reg SSOClientRegistration
	if err := jsonStr.Unmarshal(data, &reg); err != nil {
		return nil, err
	}
	return &reg, nil
}

// writeClientRegistration caches client registration
func writeClientRegistration(clientIdHash string, reg *SSOClientRegistration) error {
	path := getClientRegistrationPath(clientIdHash)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := jsonStr.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// isClientRegistrationExpired checks if client registration is expired
func isClientRegistrationExpired(reg *SSOClientRegistration) bool {
	if reg.ExpiresAt == "" {
		return true
	}
	expiresAt, err := time.Parse(time.RFC3339, reg.ExpiresAt)
	if err != nil {
		return true
	}
	// Consider expired if less than 15 minutes remaining
	return time.Now().Add(15 * time.Minute).After(expiresAt)
}

// registerSSOClient registers a new client with SSO OIDC
func registerSSOClient(startUrl, region string) (*SSOClientRegistration, error) {
	ssoOidcUrl := fmt.Sprintf("https://oidc.%s.amazonaws.com/client/register", region)

	reqBody := SSORegisterClientRequest{
		ClientName:   "Kiro IDE",
		ClientType:   "public",
		Scopes:       idcScopes,
		GrantTypes:   []string{"authorization_code", "refresh_token"},
		RedirectUris: []string{"http://127.0.0.1/oauth/callback"},
		IssuerUrl:    startUrl,
	}

	reqJson, err := jsonStr.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequest("POST", ssoOidcUrl, bytes.NewBuffer(reqJson))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("client registration failed (status %d): %s", resp.StatusCode, string(body))
	}

	var regResp SSORegisterClientResponse
	if err := jsonStr.Unmarshal(body, &regResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	reg := &SSOClientRegistration{
		ClientId:     regResp.ClientId,
		ClientSecret: regResp.ClientSecret,
		ExpiresAt:    time.Unix(regResp.ClientSecretExpiresAt, 0).Format(time.RFC3339),
	}

	return reg, nil
}

// loginIdC performs browser-based OAuth login with AWS Identity Center
func loginIdC(provider, startUrl, region string) {
	fmt.Printf("Starting login with %s (Identity Center)...\n", provider)
	fmt.Printf("Start URL: %s\n", startUrl)
	fmt.Printf("Region: %s\n", region)

	clientIdHash := getClientIdHash(startUrl)

	// Try to read existing client registration
	clientReg, err := readClientRegistration(clientIdHash)
	if err != nil || isClientRegistrationExpired(clientReg) {
		fmt.Println("Registering new client with SSO OIDC...")
		clientReg, err = registerSSOClient(startUrl, region)
		if err != nil {
			fmt.Printf("Failed to register client: %v\n", err)
			os.Exit(1)
		}
		// Cache the registration
		if err := writeClientRegistration(clientIdHash, clientReg); err != nil {
			fmt.Printf("Warning: Failed to cache client registration: %v\n", err)
		}
		fmt.Println("Client registered successfully")
	} else {
		fmt.Println("Using cached client registration")
	}

	// Generate PKCE values
	codeVerifier, codeChallenge, err := generatePKCE()
	if err != nil {
		fmt.Printf("Failed to generate PKCE: %v\n", err)
		os.Exit(1)
	}

	// Generate state for CSRF protection
	state, err := generateState()
	if err != nil {
		fmt.Printf("Failed to generate state: %v\n", err)
		os.Exit(1)
	}

	// Find available port and start local callback server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Printf("Failed to start callback server: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	redirectUri := fmt.Sprintf("http://127.0.0.1:%d/oauth/callback", port)

	fmt.Printf("Callback server started on port %d\n", port)

	// Channel to receive the auth code
	authCodeChan := make(chan string, 1)
	errChan := make(chan error, 1)

	// Create HTTP server for callback
	mux := http.NewServeMux()
	server := &http.Server{Handler: mux}

	mux.HandleFunc("/oauth/callback", func(w http.ResponseWriter, r *http.Request) {
		// Validate state
		receivedState := r.URL.Query().Get("state")
		if receivedState != state {
			errChan <- fmt.Errorf("state mismatch")
			http.Error(w, "Invalid state parameter", http.StatusBadRequest)
			return
		}

		// Check for error
		if errParam := r.URL.Query().Get("error"); errParam != "" {
			errDesc := r.URL.Query().Get("error_description")
			errChan <- fmt.Errorf("OAuth error: %s - %s", errParam, errDesc)
			http.Error(w, fmt.Sprintf("Authentication failed: %s", errDesc), http.StatusBadRequest)
			return
		}

		// Get auth code
		code := r.URL.Query().Get("code")
		if code == "" {
			errChan <- fmt.Errorf("no authorization code received")
			http.Error(w, "No authorization code received", http.StatusBadRequest)
			return
		}

		// Send success response
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`<!DOCTYPE html><html><head><meta charset="UTF-8"><title>Login Successful</title>
			<style>body{font-family:system-ui;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;background:linear-gradient(135deg,#667eea,#764ba2)}
			.box{text-align:center;background:#fff;padding:40px 60px;border-radius:16px;box-shadow:0 10px 40px rgba(0,0,0,.2)}h1{color:#333}p{color:#666}</style>
			</head><body><div class="box"><h1>✓ Login Successful!</h1><p>You can close this window.</p></div></body></html>`))

		authCodeChan <- code
	})

	// Start server in goroutine
	go func() {
		if err := server.Serve(listener); err != http.ErrServerClosed {
			errChan <- fmt.Errorf("callback server error: %v", err)
		}
	}()

	// Build SSO OIDC authorize URL
	authUrl := fmt.Sprintf("https://oidc.%s.amazonaws.com/authorize?response_type=code&client_id=%s&redirect_uri=%s&scopes=%s&state=%s&code_challenge=%s&code_challenge_method=S256",
		region,
		clientReg.ClientId,
		url.QueryEscape(redirectUri),
		url.QueryEscape(strings.Join(idcScopes, ",")),
		state,
		codeChallenge,
	)

	fmt.Println("Opening browser for authentication...")
	fmt.Printf("If the browser doesn't open, please visit:\n%s\n\n", authUrl)

	if err := openBrowser(authUrl); err != nil {
		fmt.Printf("Failed to open browser: %v\n", err)
		fmt.Println("Please open the URL above manually.")
	}

	fmt.Println("Waiting for authentication...")

	// Wait for auth code or error with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	select {
	case code := <-authCodeChan:
		server.Shutdown(context.Background())

		fmt.Println("Authorization code received, exchanging for tokens...")

		// Exchange code for tokens via SSO OIDC
		token, err := exchangeCodeForTokensIdC(code, codeVerifier, redirectUri, clientReg, clientIdHash, provider, region, startUrl)
		if err != nil {
			fmt.Printf("Failed to exchange code for tokens: %v\n", err)
			os.Exit(1)
		}

		// Save token to file
		if err := saveToken(token); err != nil {
			fmt.Printf("Failed to save token: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("\n✓ Login successful!")
		fmt.Printf("Provider: %s\n", provider)
		fmt.Printf("Region: %s\n", region)
		fmt.Printf("Token expires at: %s\n", token.ExpiresAt)
		fmt.Println("\nYou can now run 'claude2kiro server' to start the proxy.")

	case err := <-errChan:
		server.Shutdown(context.Background())
		fmt.Printf("Authentication failed: %v\n", err)
		os.Exit(1)

	case <-ctx.Done():
		server.Shutdown(context.Background())
		fmt.Println("Authentication timed out. Please try again.")
		os.Exit(1)
	}
}

// exchangeCodeForTokensIdC exchanges auth code for tokens via SSO OIDC
func exchangeCodeForTokensIdC(code, codeVerifier, redirectUri string, clientReg *SSOClientRegistration, clientIdHash, provider, region, startUrl string) (*TokenData, error) {
	tokenUrl := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	reqBody := SSOCreateTokenRequest{
		ClientId:     clientReg.ClientId,
		ClientSecret: clientReg.ClientSecret,
		GrantType:    "authorization_code",
		Code:         code,
		RedirectUri:  redirectUri,
		CodeVerifier: codeVerifier,
	}

	reqJson, err := jsonStr.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequest("POST", tokenUrl, bytes.NewBuffer(reqJson))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp SSOCreateTokenResponse
	if err := jsonStr.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}

	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	token := &TokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "IdC",
		Provider:     provider,
		ClientIdHash: clientIdHash,
		Region:       region,
		StartUrl:     startUrl,
	}

	return token, nil
}

// exchangeCodeForTokens exchanges the authorization code for access tokens (social auth)
func exchangeCodeForTokens(code, codeVerifier, redirectUri, provider string) (*TokenData, error) {
	tokenUrl := fmt.Sprintf("%s/oauth/token", kiroAuthEndpoint)

	reqBody := CreateTokenRequest{
		Code:         code,
		CodeVerifier: codeVerifier,
		RedirectUri:  redirectUri,
	}

	reqJson, err := jsonStr.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequest("POST", tokenUrl, bytes.NewBuffer(reqJson))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	// Set headers to match Kiro IDE
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp CreateTokenResponse
	if err := jsonStr.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %v", err)
	}


	// Calculate expiration time
	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	token := &TokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   "social",
		Provider:     provider,
		ProfileArn:   tokenResp.ProfileArn,
	}

	return token, nil
}

// saveToken saves the token to the token file
func saveToken(token *TokenData) error {
	tokenPath := getTokenFilePath()

	// Ensure directory exists
	dir := filepath.Dir(tokenPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	data, err := jsonStr.MarshalIndent(token, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal token: %v", err)
	}

	if err := os.WriteFile(tokenPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %v", err)
	}

	return nil
}

// readToken reads and displays token information
func readToken() {
	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("Failed to read token file: %v\n", err)
		os.Exit(1)
	}

	var token TokenData
	if err := jsonStr.Unmarshal(data, &token); err != nil {
		fmt.Printf("Failed to parse token file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Token Information:")
	fmt.Printf("Access Token: %s\n", token.AccessToken)
	fmt.Printf("Refresh Token: %s\n", token.RefreshToken)
	if token.ExpiresAt != "" {
		fmt.Printf("Expires At: %s\n", token.ExpiresAt)
	}
}

// refreshToken refreshes the access token
func refreshToken() {
	tokenPath := getTokenFilePath()

	// Read current token
	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("Failed to read token file: %v\n", err)
		os.Exit(1)
	}

	var currentToken TokenData
	if err := jsonStr.Unmarshal(data, &currentToken); err != nil {
		fmt.Printf("Failed to parse token file: %v\n", err)
		os.Exit(1)
	}

	var newToken TokenData

	// Use different refresh mechanism based on auth method
	if currentToken.AuthMethod == "IdC" {
		// IdC uses AWS SSO OIDC refresh
		newToken, err = refreshTokenIdC(currentToken)
		if err != nil {
			fmt.Printf("Token refresh failed: %v\n", err)
			os.Exit(1)
		}
	} else {
		// Social auth uses Kiro's refresh endpoint
		newToken, err = refreshTokenSocial(currentToken)
		if err != nil {
			fmt.Printf("Token refresh failed: %v\n", err)
			os.Exit(1)
		}
	}

	newData, err := jsonStr.MarshalIndent(newToken, "", "  ")
	if err != nil {
		fmt.Printf("Failed to serialize new token: %v\n", err)
		os.Exit(1)
	}

	if err := os.WriteFile(tokenPath, newData, 0600); err != nil {
		fmt.Printf("Failed to write token file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Token refresh successful!")
	tokenPreview := newToken.AccessToken
	if len(tokenPreview) > 20 {
		tokenPreview = tokenPreview[:20]
	}
	fmt.Printf("New Access Token: %s...\n", tokenPreview)
}

// refreshTokenIdC refreshes token using AWS SSO OIDC
func refreshTokenIdC(currentToken TokenData) (TokenData, error) {
	// Get client registration
	clientReg, err := readClientRegistration(currentToken.ClientIdHash)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read client registration: %v (try logging in again)", err)
	}

	region := currentToken.Region
	if region == "" {
		region = "us-east-1"
	}

	tokenUrl := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	reqBody := SSOCreateTokenRequest{
		ClientId:     clientReg.ClientId,
		ClientSecret: clientReg.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: currentToken.RefreshToken,
	}

	reqJson, err := jsonStr.Marshal(reqBody)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to marshal request: %v", err)
	}

	req, err := http.NewRequest("POST", tokenUrl, bytes.NewBuffer(reqJson))
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return TokenData{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read response body: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return TokenData{}, fmt.Errorf("token refresh failed (status %d): %s", resp.StatusCode, string(body))
	}

	var tokenResp SSOCreateTokenResponse
	if err := jsonStr.Unmarshal(body, &tokenResp); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse response: %v", err)
	}

	expiresAt := time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)

	return TokenData{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    expiresAt.Format(time.RFC3339),
		AuthMethod:   currentToken.AuthMethod,
		Provider:     currentToken.Provider,
		ClientIdHash: currentToken.ClientIdHash,
		Region:       currentToken.Region,
		StartUrl:     currentToken.StartUrl,
	}, nil
}

// refreshTokenSocial refreshes token using Kiro's social auth endpoint
func refreshTokenSocial(currentToken TokenData) (TokenData, error) {
	refreshReq := RefreshRequest{
		RefreshToken: currentToken.RefreshToken,
	}

	reqBody, err := jsonStr.Marshal(refreshReq)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to serialize request: %v", err)
	}

	resp, err := http.Post(
		"https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
		"application/json",
		bytes.NewBuffer(reqBody),
	)
	if err != nil {
		return TokenData{}, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return TokenData{}, fmt.Errorf("status code: %d (failed to read response: %v)", resp.StatusCode, err)
		}
		return TokenData{}, fmt.Errorf("status code: %d, response: %s", resp.StatusCode, string(body))
	}

	var refreshResp RefreshResponse
	if err := jsonStr.NewDecoder(resp.Body).Decode(&refreshResp); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse response: %v", err)
	}

	return TokenData{
		AccessToken:  refreshResp.AccessToken,
		RefreshToken: refreshResp.RefreshToken,
		ExpiresAt:    refreshResp.ExpiresAt,
		AuthMethod:   currentToken.AuthMethod,
		Provider:     currentToken.Provider,
		ProfileArn:   currentToken.ProfileArn,
	}, nil
}

// tryRefreshToken attempts to refresh the token without exiting on failure
// This is used by the server to handle 403 errors gracefully
// Uses a mutex to prevent concurrent refresh attempts from racing
func tryRefreshToken() error {
	tokenRefreshMutex.Lock()
	defer tokenRefreshMutex.Unlock()

	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return fmt.Errorf("failed to read token file: %v", err)
	}

	var currentToken TokenData
	if err := jsonStr.Unmarshal(data, &currentToken); err != nil {
		return fmt.Errorf("failed to parse token file: %v", err)
	}

	// Check if token was already refreshed by another goroutine while we were waiting
	if currentToken.ExpiresAt != "" {
		if expiresAt, parseErr := time.Parse(time.RFC3339, currentToken.ExpiresAt); parseErr == nil {
			if time.Until(expiresAt) > 5*time.Minute {
				// Token was recently refreshed, no need to refresh again
				return nil
			}
		}
	}

	var newToken TokenData

	if currentToken.AuthMethod == "IdC" {
		newToken, err = refreshTokenIdC(currentToken)
	} else {
		newToken, err = refreshTokenSocial(currentToken)
	}

	if err != nil {
		return err
	}

	newData, err := jsonStr.MarshalIndent(newToken, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize new token: %v", err)
	}

	if err := os.WriteFile(tokenPath, newData, 0600); err != nil {
		return fmt.Errorf("failed to write token file: %v", err)
	}

	fmt.Println("Token refreshed successfully")
	return nil
}

// exportEnvVars exports environment variables
func exportEnvVars() {
	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("Failed to read token. Please install Kiro and log in first: %v\n", err)
		os.Exit(1)
	}

	var token TokenData
	if err := jsonStr.Unmarshal(data, &token); err != nil {
		fmt.Printf("Failed to parse token file: %v\n", err)
		os.Exit(1)
	}

	// Output environment variable commands based on OS
	cfg := config.Get()
	port := cfg.Server.Port
	if runtime.GOOS == "windows" {
		fmt.Println("CMD")
		fmt.Printf("set ANTHROPIC_BASE_URL=http://localhost:%s\n", port)
		fmt.Printf("set ANTHROPIC_API_KEY=%s\n\n", token.AccessToken)
		fmt.Println("Powershell")
		fmt.Printf(`$env:ANTHROPIC_BASE_URL="http://localhost:%s"`, port)
		fmt.Println()
		fmt.Printf(`$env:ANTHROPIC_API_KEY="%s"`, token.AccessToken)
	} else {
		fmt.Printf("export ANTHROPIC_BASE_URL=http://localhost:%s\n", port)
		fmt.Printf("export ANTHROPIC_API_KEY=\"%s\"\n", token.AccessToken)
	}
}

// setClaude configures Claude Code settings
func setClaude() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		fmt.Printf("Failed to get user home directory: %v\n", err)
		os.Exit(1)
	}

	claudeJsonPath := filepath.Join(homeDir, ".claude.json")
	ok, _ := FileExists(claudeJsonPath)
	if !ok {
		fmt.Println("Claude config file not found. Please confirm Claude Code is installed.")
		fmt.Println("npm install -g @anthropic-ai/claude-code")
		os.Exit(1)
	}

	data, err := os.ReadFile(claudeJsonPath)
	if err != nil {
		fmt.Printf("Failed to read Claude config file: %v\n", err)
		os.Exit(1)
	}

	var jsonData map[string]interface{}

	err = jsonStr.Unmarshal(data, &jsonData)

	if err != nil {
		fmt.Printf("Failed to parse JSON file: %v\n", err)
		os.Exit(1)
	}

	jsonData["hasCompletedOnboarding"] = true
	jsonData["claude2kiro"] = true
	jsonData["oauthAccount"] = map[string]interface{}{
		"type":                "api_key",
		"isMaxSubscription":   false,
		"isApiKeyPrimaryAuth": true,
	}
	jsonData["primaryAccountUuid"] = "claude2kiro-local"
	jsonData["hasSeenApiKeyPrompt"] = true

	newJson, err := json.MarshalIndent(jsonData, "", "  ")

	if err != nil {
		fmt.Printf("Failed to generate JSON file: %v\n", err)
		os.Exit(1)
	}

	err = os.WriteFile(claudeJsonPath, newJson, 0644)

	if err != nil {
		fmt.Printf("Failed to write JSON file: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Claude config file updated successfully")

}

// getToken retrieves the current token
func getToken() (TokenData, error) {
	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read token file: %v", err)
	}

	var token TokenData
	if err := jsonStr.Unmarshal(data, &token); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse token file: %v", err)
	}

	return token, nil
}

// logMiddleware logs all HTTP requests
func logMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startTime := time.Now()

		fmt.Printf("\n=== Request received ===\n")
		fmt.Printf("Time: %s\n", startTime.Format("2006-01-02 15:04:05"))
		fmt.Printf("Method: %s\n", r.Method)
		fmt.Printf("Path: %s\n", r.URL.Path)
		fmt.Printf("Headers:\n")
		for name, values := range r.Header {
			fmt.Printf("  %s: %s\n", name, strings.Join(values, ", "))
		}

		// Call next handler
		next(w, r)

		// Calculate processing time (debug output removed - interferes with TUI)
		_ = time.Since(startTime)
	}
}

// startServer starts the HTTP proxy server
func startServer(port string) {
	// Create router
	mux := http.NewServeMux()

	// Register all endpoints
	mux.HandleFunc("/v1/messages", logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		// Only handle POST requests
		if r.Method != http.MethodPost {
			http.Error(w, "Only POST requests are supported", http.StatusMethodNotAllowed)
			return
		}

		// Get current token
		token, err := getToken()
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to get token: %v", err), http.StatusInternalServerError)
			return
		}

		// Read request body
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed to read request body: %v", err), http.StatusInternalServerError)
			return
		}
		defer r.Body.Close()

		// Parse Anthropic request
		var anthropicReq AnthropicRequest
		if err := jsonStr.Unmarshal(body, &anthropicReq); err != nil {
			http.Error(w, fmt.Sprintf("Failed to parse request body: %v", err), http.StatusBadRequest)
			return
		}

		// Basic validation with clear error messages
		if anthropicReq.Model == "" {
			http.Error(w, `{"message":"Missing required field: model"}`, http.StatusBadRequest)
			return
		}
		if len(anthropicReq.Messages) == 0 {
			http.Error(w, `{"message":"Missing required field: messages"}`, http.StatusBadRequest)
			return
		}
		// Get Kiro model ID (dynamic if not in static map)
		_ = getKiroModelID(anthropicReq.Model)

		// Check for Anthropic bypass/comparison modes
		cfg := config.Get()

		if cfg.Advanced.AnthropicDirect {
			// Anthropic Direct Mode: forward request unchanged to Anthropic
			resp, err := forwardToAnthropicHeadless(r, body)
			if err != nil {
				http.Error(w, fmt.Sprintf("Anthropic forward failed: %v", err), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			// Copy ALL response headers
			for k, v := range resp.Header {
				w.Header()[k] = v
			}
			w.WriteHeader(resp.StatusCode)

			// Check if this is a streaming response (SSE)
			if resp.Header.Get("Content-Type") == "text/event-stream" {
				// Read full response body to parse SSE events
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					return
				}

				// Forward events to client
				flusher, ok := w.(http.Flusher)
				if ok {
					w.Write(respBody)
					flusher.Flush()
				} else {
					w.Write(respBody)
				}
			} else {
				// Non-streaming: decompress and read entire body
				respBody, err := decompressResponse(resp)
				if err != nil {
					http.Error(w, "Failed to decompress response", http.StatusInternalServerError)
					return
				}
				w.Write(respBody)
			}
			return
		}

		if cfg.Advanced.ComparisonMode {
			// Comparison Mode: send to both Anthropic and Kiro in parallel
			// Copy headers before goroutine to avoid race condition (request may be reused)
			headersCopy := make(http.Header)
			for k, v := range r.Header {
				headersCopy[k] = v
			}
			go func(headers http.Header) {
				fmt.Println("[CMP] Sending parallel request to Anthropic")
				resp, err := forwardToAnthropicHeadlessWithHeaders(headers, body)
				if err != nil {
					fmt.Printf("[CMP] Anthropic error: %v\n", err)
					return
				}
				defer resp.Body.Close()
				respBody, err := io.ReadAll(resp.Body)
				if err != nil {
					fmt.Printf("[CMP] Failed to read Anthropic response: %v\n", err)
					return
				}

				// Extract text preview from SSE response
				preview := extractAnthropicTextPreview(string(respBody), 80)

				// Save to secure debug file
				debugFile, err := debug.WriteDebugFileWithScrub("comparison-anthropic", respBody)
				if err != nil {
					fmt.Printf("[CMP] Failed to save debug file: %v\n", err)
					return
				}
				fmt.Printf("[CMP] Anthropic: preview=%q (%d bytes) -> %s\n", preview, len(respBody), debugFile)
			}(headersCopy)
			// Continue with normal Kiro flow below...
		}

		// Handle streaming request
		if anthropicReq.Stream {
			handleStreamRequest(w, anthropicReq, token)
			return
		}

		// Handle non-streaming request
		handleNonStreamRequest(w, anthropicReq, token)
	}))

	// Add health check endpoint
	mux.HandleFunc("/health", logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	// Add 404 handler
	mux.HandleFunc("/", logMiddleware(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "404 Not Found", http.StatusNotFound)
	}))

	// Start server
	fmt.Printf("Starting Anthropic API proxy server on port: %s\n", port)
	fmt.Printf("Available endpoints:\n")
	fmt.Printf("  POST /v1/messages - Anthropic API proxy\n")
	fmt.Printf("  GET  /health      - Health check\n")
	fmt.Printf("Press Ctrl+C to stop the server\n")

	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Printf("Failed to start server: %v\n", err)
		os.Exit(1)
	}
}

// handleStreamRequest handles streaming requests
func handleStreamRequest(w http.ResponseWriter, anthropicReq AnthropicRequest, token TokenData) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported!", http.StatusInternalServerError)
		return
	}

	messageId := fmt.Sprintf("msg_%s", time.Now().Format("20060102150405"))

	// Build CodeWhisperer request
	cwReq := buildCodeWhispererRequest(anthropicReq, token)

	// Serialize request body
	cwReqBody, err := jsonStr.Marshal(cwReq)
	if err != nil {
		sendErrorEvent(w, flusher, "Failed to serialize request", err)
		return
	}

	// fmt.Printf("CodeWhisperer streaming request body:\n%s\n", string(cwReqBody))

	// Create streaming request
	cfg := config.Get()
	proxyReq, err := http.NewRequest(
		http.MethodPost,
		cfg.Advanced.CodeWhispererEndpoint,
		bytes.NewBuffer(cwReqBody),
	)
	if err != nil {
		sendErrorEvent(w, flusher, "Failed to create proxy request", err)
		return
	}

	// Set request headers
	proxyReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	proxyReq.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	// Send request
	client := &http.Client{}

	resp, err := client.Do(proxyReq)
	if err != nil {
		sendErrorEvent(w, flusher, "CodeWhisperer request error", fmt.Errorf("request error: %s", err.Error()))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		bodyStr := "(failed to read body)"
		if readErr == nil {
			bodyStr = string(body)
		}

		if resp.StatusCode == 403 {
			// Try to refresh token inline (don't exit on failure)
			if err := tryRefreshToken(); err != nil {
				sendErrorEvent(w, flusher, "error", fmt.Errorf("Token expired, refresh failed: %v. Please re-login", err))
			} else {
				sendErrorEvent(w, flusher, "error", fmt.Errorf("Token refreshed, please retry"))
			}
		} else {
			sendErrorEvent(w, flusher, "error", fmt.Errorf("CodeWhisperer Error: %s", bodyStr))
		}
		return
	}

	// Read entire response body
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		sendErrorEvent(w, flusher, "error", fmt.Errorf("CodeWhisperer Error: failed to read response"))
		return
	}

	// Use CodeWhisperer parser
	events := parser.ParseEvents(respBody)

	// Use new SSE builder if feature flag is enabled
	if cfg.Advanced.UseNewSSEBuilder {
		handleStreamRequestWithNewBuilder(w, flusher, events, anthropicReq, messageId, cfg)
		return
	}

	// --- Old code path (fallback) ---
	if len(events) > 0 {

		// Send start event
		messageStart := map[string]any{
			"type": "message_start",
			"message": map[string]any{
				"id":            messageId,
				"type":          "message",
				"role":          "assistant",
				"content":       []any{},
				"model":         anthropicReq.Model,
				"stop_reason":   nil,
				"stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  len(getMessageContent(anthropicReq.Messages[0].Content)),
					"output_tokens": 1,
				},
			},
		}
		sendSSEEvent(w, flusher, "message_start", messageStart, nil)
		sendSSEEvent(w, flusher, "ping", map[string]string{
			"type": "ping",
		}, nil)

		contentBlockStart := map[string]any{
			"content_block": map[string]any{
				"text": "",
				"type": "text"},
			"index": 0, "type": "content_block_start",
		}

		sendSSEEvent(w, flusher, "content_block_start", contentBlockStart, nil)
		// Process parsed events

		// Send parsed events, skipping message_delta (parser generates it prematurely)
		// and content_block_stop (we send it ourselves to ensure correct ordering).
		// Correct Anthropic SSE order: content_block_delta* -> content_block_stop -> message_delta -> message_stop
		for _, e := range events {
			if e.Event == "message_delta" || e.Event == "content_block_stop" || e.Event == "message_stop" {
				continue // Skip - we'll send these in the correct order below
			}
			sendSSEEvent(w, flusher, e.Event, e.Data, nil)

			// Random delay for natural streaming
			time.Sleep(time.Duration(mathrand.Intn(int(cfg.Network.StreamingDelayMax.Milliseconds()))) * time.Millisecond)
		}

		// Send closing events in correct order
		sendSSEEvent(w, flusher, "content_block_stop", map[string]any{
			"index": 0,
			"type":  "content_block_stop",
		}, nil)

		sendSSEEvent(w, flusher, "message_delta", map[string]any{
			"type":  "message_delta",
			"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
			"usage": map[string]any{"output_tokens": 0},
		}, nil)

		sendSSEEvent(w, flusher, "message_stop", map[string]any{
			"type": "message_stop",
		}, nil)
	}

}

// handleStreamRequestWithNewBuilder handles SSE streaming using the new sse.StreamWriter.
// This is used when config.Advanced.UseNewSSEBuilder is true.
func handleStreamRequestWithNewBuilder(w http.ResponseWriter, flusher http.Flusher, events []parser.SSEEvent, anthropicReq AnthropicRequest, messageId string, cfg *config.Config) {
	streamCfg := sse.StreamConfig{
		MessageID:         messageId,
		Model:             anthropicReq.Model,
		InputTokens:       len(getMessageContent(anthropicReq.Messages[0].Content)),
		StreamingDelayMax: cfg.Network.StreamingDelayMax,
	}

	delayFn := func() {
		time.Sleep(time.Duration(mathrand.Intn(int(cfg.Network.StreamingDelayMax.Milliseconds()))) * time.Millisecond)
	}

	if len(events) > 0 {
		sse.StreamEvents(w, flusher, events, streamCfg, nil, delayFn)
	}
}

// handleNonStreamRequest handles non-streaming requests
func handleNonStreamRequest(w http.ResponseWriter, anthropicReq AnthropicRequest, token TokenData) {
	// Build CodeWhisperer request
	cwReq := buildCodeWhispererRequest(anthropicReq, token)

	// Serialize request body
	cwReqBody, err := jsonStr.Marshal(cwReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to serialize request: %v", err), http.StatusInternalServerError)
		return
	}

	// Create request
	cfg := config.Get()
	proxyReq, err := http.NewRequest(
		http.MethodPost,
		cfg.Advanced.CodeWhispererEndpoint,
		bytes.NewBuffer(cwReqBody),
	)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create proxy request: %v", err), http.StatusInternalServerError)
		return
	}

	// Set request headers (same as streaming - Kiro always returns binary event stream)
	proxyReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Accept", "text/event-stream")
	proxyReq.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	// Send request
	client := &http.Client{}

	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to send request: %v", err), http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	// Read response
	cwRespBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to read response: %v", err), http.StatusInternalServerError)
		return
	}

	// fmt.Printf("CodeWhisperer response body:\n%s\n", string(cwRespBody))

	respBodyStr := string(cwRespBody)

	events := parser.ParseEvents(cwRespBody)

	textContent := ""
	toolName := ""
	toolUseId := ""
	partialJsonStr := ""

	contexts := []map[string]any{}

	// Extract content from parsed events.
	// Parser generates: content_block_delta (text_delta/input_json_delta) and message_delta.
	// Note: parser does NOT generate content_block_start or content_block_stop events.
	for _, event := range events {
		if event.Data == nil {
			continue
		}
		dataMap, ok := event.Data.(map[string]any)
		if !ok {
			continue
		}

		switch event.Event {
		case "content_block_delta":
			if delta, ok := dataMap["delta"].(map[string]any); ok {
				switch delta["type"] {
				case "text_delta":
					if text, ok := delta["text"].(string); ok {
						textContent += text
					}
				case "input_json_delta":
					if pj, ok := delta["partial_json"].(string); ok {
						partialJsonStr += pj
					}
				}
			}
			// Also check direct content field (Kiro raw format)
			if content, ok := dataMap["content"].(string); ok && content != "" {
				textContent += content
			}

		case "content_block_start":
			if cb, ok := dataMap["content_block"].(map[string]any); ok {
				if cb["type"] == "tool_use" {
					if name, ok := cb["name"].(string); ok {
						toolName = name
					}
					if id, ok := cb["id"].(string); ok {
						toolUseId = id
					}
				}
			}
		}
	}

	// Build content blocks
	if strings.TrimSpace(textContent) != "" {
		contexts = append(contexts, map[string]any{
			"type": "text",
			"text": textContent,
		})
	}
	if toolName != "" && partialJsonStr != "" {
		toolInput := map[string]any{}
		jsonStr.Unmarshal([]byte(partialJsonStr), &toolInput)
		contexts = append(contexts, map[string]any{
			"type":  "tool_use",
			"id":    toolUseId,
			"name":  toolName,
			"input": toolInput,
		})
	}
	
	// Check for error response
	if strings.Contains(string(cwRespBody), "Improperly formed request.") {
		http.Error(w, fmt.Sprintf("Request format error: %s", respBodyStr), http.StatusBadRequest)
		return
	}

	// Build Anthropic response
	anthropicResp := map[string]any{
		"content":       contexts,
		"model":         anthropicReq.Model,
		"role":          "assistant",
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"type":          "message",
		"usage": map[string]any{
			"input_tokens":  len(cwReq.ConversationState.CurrentMessage.UserInputMessage.Content),
			"output_tokens": len(textContent),
		},
	}

	// Send response
	w.Header().Set("Content-Type", "application/json")
	jsonStr.NewEncoder(w).Encode(anthropicResp)
}

// sendSSEEvent sends an SSE event and optionally captures it
func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventType string, data any, capturedEvents *[]CapturedSSEEvent) {

	json, err := jsonStr.Marshal(data)
	if err != nil {
		return
	}

	// Capture event if capturedEvents is provided
	if capturedEvents != nil {
		*capturedEvents = append(*capturedEvents, CapturedSSEEvent{
			Event: eventType,
			Data:  string(json),
		})
	}

	fmt.Fprintf(w, "event: %s\n", eventType)
	fmt.Fprintf(w, "data: %s\n\n", string(json))
	flusher.Flush()

}

// sendErrorEvent sends an error event
func sendErrorEvent(w http.ResponseWriter, flusher http.Flusher, message string, err error) {
	// Include error details in the message
	fullMessage := message
	if err != nil {
		fullMessage = fmt.Sprintf("%s: %v", message, err)
	}

	errorResp := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "overloaded_error",
			"message": fullMessage,
		},
	}

	sendSSEEvent(w, flusher, "error", errorResp, nil)
}

// FileExists checks if a file or directory exists
func FileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil // File or directory exists
	}
	if os.IsNotExist(err) {
		return false, nil // File or directory does not exist
	}
	return false, err // Other error
}
