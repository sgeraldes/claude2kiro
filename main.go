package main

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	jsonStr "encoding/json"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/manifoldco/promptui"

	"github.com/sgeraldes/claude2kiro/cmd"
	"github.com/sgeraldes/claude2kiro/internal/config"
	webdash "github.com/sgeraldes/claude2kiro/internal/dashboard"
	"github.com/sgeraldes/claude2kiro/internal/debug"
	"github.com/sgeraldes/claude2kiro/internal/models"

	"github.com/sgeraldes/claude2kiro/internal/tui"
	"github.com/sgeraldes/claude2kiro/internal/tui/dashboard"
	"github.com/sgeraldes/claude2kiro/internal/tui/logger"
	"github.com/sgeraldes/claude2kiro/internal/tui/menu"
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
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl map[string]any `json:"cache_control,omitempty"`
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
	ToolSpecification *ToolSpecification `json:"toolSpecification,omitempty"`
	CachePoint        *CachePoint        `json:"cachePoint,omitempty"`
}

// CachePoint is the Kiro/CodeWhisperer equivalent of an Anthropic cache_control
// breakpoint. Placed as its own entry in the tools array after a tool, it marks a
// caching boundary so the backend can reuse the preceding tool definitions.
type CachePoint struct {
	Type string `json:"type"`
}

// HistoryUserMessage represents a user message in conversation history
type HistoryUserMessage struct {
	UserInputMessage struct {
		Content                 string              `json:"content"`
		ModelId                 string              `json:"modelId"`
		Origin                  string              `json:"origin"`
		UserInputMessageContext *HistoryUserContext `json:"userInputMessageContext,omitempty"`
	} `json:"userInputMessage"`
}

// HistoryUserContext holds tool results for history messages
type HistoryUserContext struct {
	ToolResults []ToolResult `json:"toolResults,omitempty"`
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
	} `json:"delta"`
	Content []struct {
		Text string `json:"text"`
		Type string `json:"type"`
	} `json:"content,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
	StopSequence string `json:"stop_sequence,omitempty"`
	Usage        struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
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
	Type      string       `json:"type"`
	Text      *string      `json:"text,omitempty"`
	ToolUseId *string      `json:"tool_use_id,omitempty"`
	Content   *string      `json:"content,omitempty"`
	Name      *string      `json:"name,omitempty"`
	Input     *any         `json:"input,omitempty"`
	Source    *ImageSource `json:"source,omitempty"` // For image content blocks
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
	if slices.Contains(garbageContent, content) {
		return "" // Replace with empty string
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
	case []any:
		var texts []string
		hasToolUse := false
		hasToolResult := false
		for _, block := range v {
			if m, ok := block.(map[string]any); ok {
				// Get the block type
				blockType, _ := m["type"].(string)
				switch blockType {
				case "tool_result":
					hasToolResult = true
					// Tool result content can be a string or an array of content blocks
					if contentStr, ok := m["content"].(string); ok {
						texts = append(texts, contentStr)
					} else if contentArr, ok := m["content"].([]any); ok {
						// Content is an array of blocks - extract text from each
						for _, innerBlock := range contentArr {
							if innerMap, ok := innerBlock.(map[string]any); ok {
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

	blocks, ok := content.([]any)
	if !ok {
		return toolUses
	}

	for _, block := range blocks {
		if m, ok := block.(map[string]any); ok {
			if typeVal, ok := m["type"].(string); ok && typeVal == "tool_use" {
				toolUse := HistoryToolUse{}
				if name, ok := m["name"].(string); ok {
					// Sanitize to match the (sanitized) tool-definition names so
					// history references stay consistent with the current tools.
					toolUse.Name = sanitizeToolName(name)
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

// getMessageToolResults extracts tool_result blocks from a user message content
func getMessageToolResults(content any) []ToolResult {
	var results []ToolResult

	blocks, ok := content.([]any)
	if !ok {
		return results
	}

	for _, block := range blocks {
		m, ok := block.(map[string]any)
		if !ok {
			continue
		}
		typeVal, _ := m["type"].(string)
		if typeVal != "tool_result" {
			continue
		}

		tr := ToolResult{}
		if id, ok := m["tool_use_id"].(string); ok {
			tr.ToolUseId = id
		}
		// Determine status from is_error field
		if isErr, ok := m["is_error"].(bool); ok && isErr {
			tr.Status = "error"
		} else {
			tr.Status = "success"
		}
		// Extract content - can be string or array of blocks
		switch c := m["content"].(type) {
		case string:
			tr.Content = []ToolResultContent{{Text: c}}
		case []any:
			for _, item := range c {
				if itemMap, ok := item.(map[string]any); ok {
					if text, ok := itemMap["text"].(string); ok {
						tr.Content = append(tr.Content, ToolResultContent{Text: text})
					}
				}
			}
		}
		if tr.Content == nil {
			tr.Content = []ToolResultContent{{Text: ""}}
		}
		results = append(results, tr)
	}

	return results
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
				Content                 string       `json:"content"`
				ModelId                 string       `json:"modelId"`
				Origin                  string       `json:"origin"`
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
// kiroRequestSema limits concurrent requests to Kiro backend.
// Initialized lazily from config on first use.
var kiroRequestSema chan struct{}
var kiroSemaOnce sync.Once

func getKiroSema() chan struct{} {
	kiroSemaOnce.Do(func() {
		cfg := config.Get()
		size := cfg.Network.MaxConcurrentReqs
		if size < 1 {
			size = 4
		}
		kiroRequestSema = make(chan struct{}, size)
	})
	return kiroRequestSema
}

// ModelMap translates Anthropic model IDs (sent by Claude Code) to Kiro model IDs.
// Kiro model IDs discovered via GET /ListAvailableModels?origin=AI_EDITOR
var ModelMap = map[string]string{
	// Auto - lets Kiro choose the best model (1.0x credits)
	"auto": "auto",
	// Claude Opus 4.8 (2.2x credits, 1M context, 128K output) - experimental preview
	"claude-opus-4-8": "claude-opus-4.8",
	"claude-opus-4.8": "claude-opus-4.8",
	// Claude Opus 4.7 (2.2x credits, 1M context, 128K output) - experimental preview
	"claude-opus-4-7": "claude-opus-4.7",
	"claude-opus-4.7": "claude-opus-4.7",
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
	"deepseek-3.2":  "deepseek-3.2",
	"deepseek-3-2":  "deepseek-3.2",
	"deepseek-v3.2": "deepseek-3.2",
	"deepseek":      "deepseek-3.2",
	// MiniMax M2.5 (0.25x credits, 196K context, text only)
	"minimax-m2.5": "minimax-m2.5",
	"minimax-m2-5": "minimax-m2.5",
	"minimax":      "minimax-m2.5",
	// MiniMax M2.1 (0.15x credits, 196K context)
	"minimax-m2.1": "minimax-m2.1",
	"minimax-m2-1": "minimax-m2.1",
	// Qwen3 Coder Next (0.05x credits, 256K context)
	"qwen3-coder-next": "qwen3-coder-next",
	"qwen3":            "qwen3-coder-next",
	"qwen":             "qwen3-coder-next",
	// GLM 5 (0.5x credits, 200K context, text only)
	"glm-5": "glm-5",
	"glm5":  "glm-5",
	"glm":   "glm-5",
}

// getKiroModelID converts an Anthropic model name to a Kiro model ID.
//
// Resolution order:
//  1. Curated static ModelMap (exact match) - fast, offline, handles legacy remaps.
//  2. Normalized form against the live catalog - lets a brand-new Claude version
//     (e.g. a freshly released Opus) route correctly the moment Kiro exposes it,
//     with no code change. "claude-opus-4-8" -> "claude-opus-4.8" -> available? use it.
//  3. Raw lowercased id against the live catalog - if Claude Code already sent a
//     Kiro-style id.
//  4. Family resolution from the live catalog - highest available version in the
//     requested family (opus/sonnet/haiku/...).
//  5. Static family fallback - newest known stable when the catalog is unreachable.
//  6. Pass through as-is.
func getKiroModelID(anthropicModel string) string {
	// 1. Curated static map first.
	if kiroModel, ok := ModelMap[anthropicModel]; ok {
		return kiroModel
	}

	// 2. Normalize to Kiro's dotted form and check the live catalog.
	if normalized := models.NormalizeAnthropicID(anthropicModel); normalized != "" {
		if modelCatalog.Has(normalized) {
			return normalized
		}
	}

	// 3. Maybe Claude Code already sent a Kiro-style id (e.g. "glm-5").
	lower := strings.ToLower(anthropicModel)
	if modelCatalog.Has(lower) {
		return lower
	}

	// 4. Family resolution from the live catalog: pick the highest available
	//    version in the requested family.
	for _, family := range []string{"opus", "haiku", "sonnet", "deepseek", "minimax", "qwen", "glm"} {
		if strings.Contains(lower, family) {
			if id, ok := modelCatalog.ResolveFamily(family); ok {
				return id
			}
			break
		}
	}

	// 5. Static family fallback (catalog unreachable): newest known stable model.
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
	case strings.Contains(lower, "glm"):
		return "glm-5"
	}

	// 6. Last resort: pass through as-is (Kiro may accept it).
	return anthropicModel
}

// staticModelList returns a curated list of Kiro models built from the static
// ModelMap, for model discovery when the live catalog is unreachable (offline
// or not yet warmed). It returns the distinct Kiro backend IDs the map targets,
// preferring Claude models, so Claude Desktop's picker still gets valid IDs.
func staticModelList() []models.KiroModel {
	// Curated order: newest Claude first, then non-Claude, matching the doc.
	ordered := []struct{ id, name string }{
		{"claude-opus-4.8", "Claude Opus 4.8"},
		{"claude-opus-4.7", "Claude Opus 4.7"},
		{"claude-opus-4.6", "Claude Opus 4.6"},
		{"claude-opus-4.5", "Claude Opus 4.5"},
		{"claude-sonnet-4.6", "Claude Sonnet 4.6"},
		{"claude-sonnet-4.5", "Claude Sonnet 4.5"},
		{"claude-sonnet-4", "Claude Sonnet 4"},
		{"claude-haiku-4.5", "Claude Haiku 4.5"},
		{"deepseek-3.2", "DeepSeek 3.2"},
		{"minimax-m2.5", "MiniMax M2.5"},
		{"minimax-m2.1", "MiniMax M2.1"},
		{"qwen3-coder-next", "Qwen3 Coder Next"},
		{"glm-5", "GLM 5"},
	}
	list := make([]models.KiroModel, 0, len(ordered))
	for _, m := range ordered {
		var km models.KiroModel
		km.ModelID = m.id
		km.ModelName = m.name
		list = append(list, km)
	}
	return list
}

// modelCatalog is the dynamic layer over ListAvailableModels. It caches the live
// model list (10-minute TTL) and serves stale data on fetch failure so model
// resolution on the request path never breaks.
var modelCatalog = models.NewCatalog(10*time.Minute, fetchKiroModels)

// fetchKiroModels retrieves the current model list from CodeWhisperer using the
// saved auth token and configured endpoint.
func fetchKiroModels() ([]models.KiroModel, error) {
	token, err := getToken()
	if err != nil {
		return nil, err
	}
	cfg := config.Get()
	ua := fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS)
	return models.Fetch(
		cfg.Advanced.CodeWhispererEndpoint,
		token.AccessToken,
		token.ProfileArn,
		ua,
		cfg.Network.HTTPTimeout,
	)
}

// refreshTokenIfStale refreshes the saved token when it is expired or expiring
// within 5 minutes, mirroring what `claude2kiro run` does at startup. Returns
// an error only when a refresh was needed and failed; a missing token is not
// an error here (the caller's API call will report it with context).
func refreshTokenIfStale() error {
	token, err := getToken()
	if err != nil || token.ExpiresAt == "" {
		return nil
	}
	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil || time.Until(expiresAt) >= 5*time.Minute {
		return nil
	}
	return tryRefreshToken()
}

// modelResolveInfo describes how an Anthropic-style model id routes to Kiro
// and whether the resolved model exists in this account's live catalog.
type modelResolveInfo struct {
	Requested  string  `json:"requested"`
	KiroModel  string  `json:"kiro_model"`
	InCatalog  bool    `json:"in_live_catalog"`
	Multiplier float64 `json:"credit_multiplier,omitempty"`
	MaxInput   int     `json:"max_input_tokens,omitempty"`
	MaxOutput  int     `json:"max_output_tokens,omitempty"`
	Note       string  `json:"note,omitempty"`
}

// resolveModelInfo resolves a model id exactly like the request path does and
// annotates it with this account's live-catalog data. Model availability is
// per Kiro account/plan, so in_live_catalog=false means Kiro will likely
// reject the model for this user even though the proxy forwards it.
func resolveModelInfo(requested string) modelResolveInfo {
	info := modelResolveInfo{Requested: requested, KiroModel: getKiroModelID(requested)}
	for _, m := range modelCatalog.Models() {
		if m.ModelID == info.KiroModel {
			info.InCatalog = true
			info.Multiplier = m.RateMultiplier
			info.MaxInput = m.TokenLimits.MaxInputTokens
			info.MaxOutput = m.TokenLimits.MaxOutputTokens
			break
		}
	}
	if !info.InCatalog {
		info.Note = "resolved model is not in this account's live Kiro model list; Kiro may reject it (availability varies by account/plan)"
	}
	return info
}

// printResolve shows how a model id routes to Kiro and whether this account
// can use it. Usage: claude2kiro resolve <model-id>
func printResolve() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "Usage: claude2kiro resolve <model-id>")
		os.Exit(1)
	}
	if err := refreshTokenIfStale(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: token refresh failed (%v); answering from the static map only.\n", err)
	}
	info := resolveModelInfo(os.Args[2])
	fmt.Printf("%s -> %s\n", info.Requested, info.KiroModel)
	if info.InCatalog {
		fmt.Printf("available on this account: yes (%gx credits, %d in / %d out)\n",
			info.Multiplier, info.MaxInput, info.MaxOutput)
	} else {
		fmt.Println("available on this account: NO — not in the live model list; Kiro will likely reject it")
		fmt.Println("run 'claude2kiro models' to see the models this account can use")
	}
}

// printModels fetches the live model list from Kiro and prints it. This is the
// user-facing view of the dynamic model catalog.
func printModels() {
	// Refresh an expired (or about-to-expire) token first, same as `run` does,
	// so the command works without a running proxy session.
	if err := refreshTokenIfStale(); err != nil {
		fmt.Fprintf(os.Stderr, "Token refresh failed: %v\nRun 'claude2kiro login' to re-authenticate.\n", err)
		os.Exit(1)
	}

	list, err := fetchKiroModels()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching models: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("%d models available via Kiro (live from ListAvailableModels):\n\n", len(list))
	fmt.Printf("%-20s %-22s %6s %9s %9s  %s\n", "MODEL ID", "NAME", "RATE", "MAX IN", "MAX OUT", "INPUTS")
	fmt.Printf("%-20s %-22s %6s %9s %9s  %s\n", "--------", "----", "----", "------", "-------", "------")
	for _, m := range list {
		inputs := strings.Join(m.SupportedInputTypes, "+")
		fmt.Printf("%-20s %-22s %5.2fx %9d %9d  %s\n",
			m.ModelID, m.ModelName, m.RateMultiplier,
			m.TokenLimits.MaxInputTokens, m.TokenLimits.MaxOutputTokens, inputs)
	}
	fmt.Println("\nClaude Code model IDs are resolved to these via the static map + family fallback.")

	// Keep the installed /models slash command in sync with what we just fetched.
	updateModelsDoc(list)
}

// pluginCommandDirs returns the installed kiro-proxy plugin "commands" directories
// that hold the slash-command markdown files. It includes the version-independent
// marketplace copy, this build's version cache, and every other already-installed
// version cache found on disk — so the doc is refreshed even when the running
// binary's version differs from the active install.
func pluginCommandDirs() []string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(homeDir, ".claude", "plugins")
	cacheBase := filepath.Join(base, "cache", "claude2kiro", "kiro-proxy")

	seen := map[string]bool{}
	var dirs []string
	add := func(d string) {
		if d != "" && !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}

	add(filepath.Join(base, "marketplaces", "claude2kiro", "kiro-proxy", "commands"))
	add(filepath.Join(cacheBase, menu.Version, "commands"))

	// Pick up any already-installed version caches (e.g. the active release).
	if entries, err := os.ReadDir(cacheBase); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				add(filepath.Join(cacheBase, e.Name(), "commands"))
			}
		}
	}
	return dirs
}

// updateModelsDoc regenerates the installed /models slash command from a live
// model list and writes it to each plugin command directory, but only when the
// content actually differs. This is the change-driven updater: it is wired to
// modelCatalog.OnChange so the doc tracks Kiro's (roughly weekly) model changes
// automatically, and it overwrites the embedded static copy on first fetch.
// Best-effort and silent (like installPlugin) so it never disrupts the TUI.
func updateModelsDoc(list []models.KiroModel) {
	if len(list) == 0 {
		return
	}
	content := models.RenderMarkdown(list)
	for _, dir := range pluginCommandDirs() {
		path := filepath.Join(dir, "models.md")
		if existing, err := os.ReadFile(path); err == nil && string(existing) == content {
			continue // already up to date
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			continue
		}
		_ = os.WriteFile(path, []byte(content), 0644)
	}
}

// cryptoRandIntn returns a random int in [0, n) using crypto/rand.
func cryptoRandIntn(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

// generateUUID generates a simple UUID v4
func generateUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant bits
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// uuidFromBytes formats 16 bytes as a canonical, well-formed UUID string
// (deterministic) with version/variant nibbles set.
func uuidFromBytes(b []byte) string {
	if len(b) < 16 {
		return generateUUID()
	}
	out := make([]byte, 16)
	copy(out, b[:16])
	out[6] = (out[6] & 0x0f) | 0x50 // Version 5 (name-based)
	out[8] = (out[8] & 0x3f) | 0x80 // Variant bits
	return fmt.Sprintf("%x-%x-%x-%x-%x", out[0:4], out[4:6], out[6:8], out[8:10], out[10:])
}

// stableConversationID returns a conversationId that is STABLE across all turns
// of the same client session, mirroring the real Kiro/Amazon Q IDE (which reuses
// the server-assigned conversationId for the lifetime of a chat session).
//
// Claude Code sends a per-session UUID inside metadata.user_id
// (".._session_<uuid>"); we hash it to a deterministic, well-formed UUID so the
// same session always maps to the same conversationId, letting the Kiro backend
// reuse server-side context keyed by conversationId instead of treating every
// turn as a new conversation. With no session key (non-Claude-Code clients,
// missing metadata) it falls back to a fresh random UUID — preserving prior behavior.
func stableConversationID(sessionKey string) string {
	if sessionKey == "" {
		return generateUUID()
	}
	sum := sha256.Sum256([]byte("claude2kiro-conv:" + sessionKey))
	return uuidFromBytes(sum[:])
}

// extractImagesFromContent extracts images from Anthropic message content
func extractImagesFromContent(content any) []ImageBlock {
	var images []ImageBlock

	contentBlocks, ok := content.([]any)
	if !ok {
		return images
	}

	for _, block := range contentBlocks {
		blockMap, ok := block.(map[string]any)
		if !ok {
			continue
		}

		blockType, _ := blockMap["type"].(string)
		if blockType != "image" {
			continue
		}

		source, ok := blockMap["source"].(map[string]any)
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

// resolveSchemaRefs resolves $ref references by inlining definitions from $defs.
// This must be called BEFORE stripping $defs/$ref to avoid leaving empty objects.
func resolveSchemaRefs(schema map[string]any) map[string]any {
	if schema == nil {
		return schema
	}
	// Extract $defs from the root schema
	defs, _ := schema["$defs"].(map[string]any)
	if defs == nil {
		return schema
	}
	// Resolve all refs in the schema tree
	return resolveRefs(schema, defs)
}

// resolveRefs recursively replaces {"$ref": "#/$defs/Name"} with the inlined definition.
func resolveRefs(node map[string]any, defs map[string]any) map[string]any {
	if node == nil {
		return nil
	}
	// If this node IS a $ref, resolve it
	if ref, ok := node["$ref"].(string); ok {
		// Parse "#/$defs/Name" format
		const prefix = "#/$defs/"
		if strings.HasPrefix(ref, prefix) {
			defName := ref[len(prefix):]
			if def, ok := defs[defName].(map[string]any); ok {
				// Return a copy of the definition (resolved recursively)
				return resolveRefs(copyMap(def), defs)
			}
		}
		// Unresolvable ref - return as-is (will be stripped later)
		return node
	}

	resolved := make(map[string]any, len(node))
	for k, v := range node {
		switch val := v.(type) {
		case map[string]any:
			resolved[k] = resolveRefs(val, defs)
		case []any:
			resolvedArr := make([]any, len(val))
			for i, item := range val {
				if itemMap, ok := item.(map[string]any); ok {
					resolvedArr[i] = resolveRefs(itemMap, defs)
				} else {
					resolvedArr[i] = item
				}
			}
			resolved[k] = resolvedArr
		default:
			resolved[k] = v
		}
	}
	return resolved
}

// copyMap creates a shallow copy of a map
func copyMap(m map[string]any) map[string]any {
	c := make(map[string]any, len(m))
	maps.Copy(c, m)
	return c
}

// cleanToolSchema removes JSON Schema meta-fields that Kiro/CodeWhisperer rejects.
// Fields like $schema, title, $defs are valid JSON Schema but not supported by the CW API.
// NOTE: $ref references are resolved BEFORE calling this (see resolveSchemaRefs).
func cleanToolSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return schema
	}
	cleaned := make(map[string]any, len(schema))
	for k, v := range schema {
		// Skip unsupported JSON Schema meta-fields.
		switch k {
		case "$schema", "title", "$defs", "$id", "$comment", "$ref":
			continue
		}
		// Recursively clean nested objects (e.g., properties contain schemas)
		if nested, ok := v.(map[string]any); ok {
			cleaned[k] = cleanToolSchema(nested)
		} else if arr, ok := v.([]any); ok {
			// Clean arrays (e.g., anyOf, allOf, oneOf contain schema objects)
			cleanedArr := make([]any, len(arr))
			for i, item := range arr {
				if itemMap, ok := item.(map[string]any); ok {
					cleanedArr[i] = cleanToolSchema(itemMap)
				} else {
					cleanedArr[i] = item
				}
			}
			cleaned[k] = cleanedArr
		} else {
			cleaned[k] = v
		}
	}
	return cleaned
}

// maxToolNameLen is CodeWhisperer/Bedrock's hard limit on tool names. Requests
// with a longer tool name are rejected with "Improperly formed request." Claude
// Code's MCP tool names (e.g. "mcp__plugin_<server>_<server>__<tool>") routinely
// exceed this, so the proxy must sanitize them.
const maxToolNameLen = 64

// invalidToolNameChars matches characters not allowed in a CodeWhisperer tool name
// (the allowed set is [a-zA-Z0-9_-]).
var invalidToolNameChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// sanitizeToolName returns a CodeWhisperer-safe tool name: at most maxToolNameLen
// characters, all within [a-zA-Z0-9_-]. It is deterministic — the same input
// always yields the same output — so tool definitions and the tool_use names in
// conversation history stay consistent. Uniqueness for shortened names is
// preserved via an 8-hex-char hash suffix derived from the full original name.
func sanitizeToolName(name string) string {
	safe := invalidToolNameChars.ReplaceAllString(name, "_")
	if len(safe) <= maxToolNameLen {
		return safe
	}
	sum := sha256.Sum256([]byte(name))
	suffix := "_" + hex.EncodeToString(sum[:])[:8] // 9 chars, e.g. "_1a2b3c4d"
	return safe[:maxToolNameLen-len(suffix)] + suffix
}

// buildToolNameMap returns a map from sanitized tool name back to the original,
// for every tool whose name had to be changed. It is used to restore the original
// names in the model's tool_use responses so Claude Code recognizes its tools.
func buildToolNameMap(tools []AnthropicTool) map[string]string {
	if len(tools) == 0 {
		return nil
	}
	m := make(map[string]string)
	for _, t := range tools {
		if s := sanitizeToolName(t.Name); s != t.Name {
			m[s] = t.Name
		}
	}
	return m
}

// restoreToolNames rewrites sanitized tool_use names back to their originals in
// parsed SSE events, so Claude Code sees the tool names it sent. nameMap is
// sanitized->original (from buildToolNameMap).
func restoreToolNames(events []parser.SSEEvent, nameMap map[string]string) {
	if len(nameMap) == 0 {
		return
	}
	for _, e := range events {
		if e.Event != "content_block_start" {
			continue
		}
		data, ok := e.Data.(map[string]any)
		if !ok {
			continue
		}
		cb, ok := data["content_block"].(map[string]any)
		if !ok {
			continue
		}
		if cb["type"] != "tool_use" {
			continue
		}
		if name, ok := cb["name"].(string); ok {
			if orig, ok := nameMap[name]; ok {
				cb["name"] = orig
			}
		}
	}
}

// consumerProfileArn is Kiro's shared CodeWhisperer profile for individual
// (social-auth) subscriptions: GitHub / Google / Builder ID. IdC/Enterprise
// users must use their own account-specific profile instead.
const consumerProfileArn = "arn:aws:codewhisperer:us-east-1:699475941385:profile/EHGA3GRVQMUK"

// kiroProfileFilePaths returns the candidate paths to Kiro IDE's stored profile.json.
func kiroProfileFilePaths() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	rel := filepath.Join("Kiro", "User", "globalStorage", "kiro.kiroagent", "profile.json")
	return []string{
		filepath.Join(home, "AppData", "Roaming", rel),             // Windows
		filepath.Join(home, "Library", "Application Support", rel), // macOS
		filepath.Join(home, ".config", rel),                        // Linux
	}
}

// readKiroProfileArn reads the profileArn that Kiro IDE itself selected, from its
// globalStorage. This is the authoritative source (exactly the profile Kiro uses),
// so it is preferred over API discovery. Returns "" if not found.
func readKiroProfileArn() string {
	for _, p := range kiroProfileFilePaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var prof struct {
			Arn string `json:"arn"`
		}
		if json.Unmarshal(data, &prof) == nil && prof.Arn != "" {
			return prof.Arn
		}
	}
	return ""
}

// arnAccount extracts the AWS account id from an ARN
// (arn:partition:service:region:ACCOUNT:resource). Returns "" if malformed.
func arnAccount(arn string) string {
	parts := strings.Split(arn, ":")
	if len(parts) < 5 {
		return ""
	}
	return parts[4]
}

// kiroProfile is one entry from the ListAvailableProfiles API.
type kiroProfile struct {
	Arn             string `json:"arn"`
	ProfileName     string `json:"profileName"`
	IdentityDetails struct {
		SsoIdentityDetails struct {
			OidcClientId string `json:"oidcClientId"`
		} `json:"ssoIdentityDetails"`
	} `json:"identityDetails"`
}

// fetchProfilesFromAPI calls ListAvailableProfiles and returns the profiles.
func fetchProfilesFromAPI(accessToken string) []kiroProfile {
	cfg := config.Get()
	endpoint := cfg.Advanced.ProfilesEndpoint
	if endpoint == "" {
		return nil
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString("{}"))
	if err != nil {
		return nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("User-Agent", fmt.Sprintf("KiroIDE-%s-%s", kiroVersion, runtime.GOOS))

	client := &http.Client{Timeout: cfg.Network.HTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil
	}
	var r struct {
		Profiles []kiroProfile `json:"profiles"`
	}
	if json.Unmarshal(body, &r) != nil {
		return nil
	}
	return r.Profiles
}

// selectProfileArn picks the best profile ARN from the available list:
//   - exactly one profile -> use it
//   - multiple -> the profile whose account matches its own SSO oidcClientId
//     account (the org/enterprise profile living in the identity-center account)
//   - otherwise -> "" (ambiguous: never guess, since a wrong profile breaks
//     model access)
func selectProfileArn(profiles []kiroProfile) string {
	if len(profiles) == 1 {
		return profiles[0].Arn
	}
	var matches []string
	for _, p := range profiles {
		ssoAcct := arnAccount(p.IdentityDetails.SsoIdentityDetails.OidcClientId)
		if ssoAcct != "" && ssoAcct == arnAccount(p.Arn) {
			matches = append(matches, p.Arn)
		}
	}
	if len(matches) == 1 {
		return matches[0]
	}
	return ""
}

// discoverProfileArn finds the user's CodeWhisperer profile ARN: first from Kiro
// IDE's stored profile (authoritative), then via the ListAvailableProfiles API
// with a conservative selection. Returns "" if none can be determined safely.
func discoverProfileArn(accessToken string) string {
	if arn := readKiroProfileArn(); arn != "" {
		return arn
	}
	return selectProfileArn(fetchProfilesFromAPI(accessToken))
}

// buildCodeWhispererRequest builds a CodeWhisperer request from an Anthropic request
func buildCodeWhispererRequest(anthropicReq AnthropicRequest, token TokenData) CodeWhispererRequest {
	cwReq := CodeWhispererRequest{}

	// Set ProfileArn.
	//
	// The Kiro backend associates each request with a CodeWhisperer profile. For
	// IdC/Enterprise users the correct profile is account-specific and is
	// discovered + persisted on the token (see discoverProfileArn). Sending the
	// WRONG profile is worse than sending none — it 403s or silently loses model
	// access — so we never blindly fall back to the shared consumer profile for
	// IdC. Social auth (GitHub/Google/Builder ID) uses the shared consumer profile.
	switch {
	case token.ProfileArn != "":
		cwReq.ProfileArn = token.ProfileArn
	case token.AuthMethod != "IdC":
		cwReq.ProfileArn = consumerProfileArn
	default:
		// IdC with no discovered profile: omit (works for tokens the backend
		// doesn't yet require a profile for).
		cwReq.ProfileArn = ""
	}
	cfg := config.Get()
	cwReq.ConversationState.ChatTriggerType = "MANUAL"
	// conversationId selection.
	//
	// By DEFAULT we send a fresh random UUID per request (original behavior): the
	// proxy always re-sends the full history array, so each request is fully
	// self-contained.
	//
	// OPT-IN (cfg.Advanced.StableConversationID): derive a STABLE conversationId
	// from the client session (Claude Code sends a per-session UUID in
	// metadata.user_id) so a backend that retains context server-side per
	// conversationId can reuse it across turns. This is off by default because the
	// backend's server-side retention is unverified — if it DOES retain, pairing it
	// with our full-history sending would double the ingested context (more credits,
	// not fewer), and Claude Code's /clear reuses the same session UUID, so two
	// logical conversations could collapse onto one conversationId.
	if _, sessionKey := extractSessionID(anthropicReq.Metadata); cfg.Advanced.StableConversationID && sessionKey != "" {
		cwReq.ConversationState.ConversationId = stableConversationID(sessionKey)
	} else {
		cwReq.ConversationState.ConversationId = generateUUID()
	}
	cwReq.ConversationState.CurrentMessage.UserInputMessage.Content = getMessageContent(anthropicReq.Messages[len(anthropicReq.Messages)-1].Content)
	cwReq.ConversationState.CurrentMessage.UserInputMessage.ModelId = getKiroModelID(anthropicReq.Model)
	cwReq.ConversationState.CurrentMessage.UserInputMessage.Origin = "AI_EDITOR"
	// Process tools information
	// CodeWhisperer has limits: ~10KB per tool description, ~90 tools max (~260KB body limit)
	const maxToolDescLength = 10000
	maxTools := cfg.Network.MaxToolsPerRequest
	if maxTools < 1 {
		maxTools = 85
	}
	if len(anthropicReq.Tools) > 0 {
		var tools []CodeWhispererTool
		toolsToProcess := anthropicReq.Tools
		if len(toolsToProcess) > maxTools {
			// Silently truncate - logged by caller if logger is available
			toolsToProcess = toolsToProcess[:maxTools]
		}
		for _, tool := range toolsToProcess {
			// Truncate long descriptions to avoid 400 errors
			desc := tool.Description
			if len(desc) > maxToolDescLength {
				desc = desc[:maxToolDescLength] + "...(truncated)"
			}
			// Clean the input schema: remove fields that Kiro/CodeWhisperer rejects
			// ($schema, title, $defs are JSON Schema meta-fields not supported by CW)
			// First resolve $ref references by inlining $defs, then clean meta-fields
			resolvedSchema := resolveSchemaRefs(tool.InputSchema)
			cleanedSchema := cleanToolSchema(resolvedSchema)
			cwTool := CodeWhispererTool{
				ToolSpecification: &ToolSpecification{
					// CodeWhisperer rejects tool names longer than 64 chars; sanitize
					// (deterministically) so long MCP names don't break the whole request.
					Name:        sanitizeToolName(tool.Name),
					Description: desc,
					InputSchema: InputSchema{Json: cleanedSchema},
				},
			}
			tools = append(tools, cwTool)
			// Enforce the backend tool-array limit on TOTAL emitted entries. The
			// up-front truncation only counts source tools; cachePoint entries are
			// added here, so without this the array (tools + cachePoints) could
			// exceed maxTools (~90 max / ~260KB body limit).
			if len(tools) >= maxTools {
				break
			}
			// Mirror Anthropic cache_control: emit a Kiro cachePoint entry right
			// after a tool that carried a cache breakpoint, so the backend can
			// cache the preceding tool definitions instead of re-ingesting them.
			// Only add it if there's still room under the limit.
			if tool.CacheControl != nil && len(tools) < maxTools {
				tools = append(tools, CodeWhispererTool{CachePoint: &CachePoint{Type: "default"}})
			}
		}
		cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools = tools
	}

	// Extract images and tool_results from the current message
	if len(anthropicReq.Messages) > 0 {
		lastMsg := anthropicReq.Messages[len(anthropicReq.Messages)-1]
		images := extractImagesFromContent(lastMsg.Content)
		if len(images) > 0 {
			cwReq.ConversationState.CurrentMessage.UserInputMessage.Images = images
		}
		// If the current message contains tool_result blocks, extract them
		toolResults := getMessageToolResults(lastMsg.Content)
		if len(toolResults) > 0 {
			cwReq.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.ToolResults = toolResults
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

		// Process regular message history with full tool use/result support.
		// Claude Code does NOT guarantee strict user/assistant alternation: it may
		// interleave a role:"system" message (e.g. injected reminders) between a
		// user turn and the assistant's tool_use. CodeWhisperer requires (a)
		// alternating user/assistant turns and (b) that every tool_result's
		// tool_use_id has a matching tool_use in a PRIOR assistant turn. The old
		// loop paired user→assistant via lookahead, so an interposed system message
		// caused the assistant tool_use to be silently dropped — and the matching
		// tool_result then failed with "Improperly formed request".
		//
		// We instead handle each message by role and emit through a helper that
		// inserts a synthetic filler turn whenever two same-role turns would be
		// adjacent, so an assistant tool_use is never dropped.
		defaultUserMsg := HistoryUserMessage{}
		defaultUserMsg.UserInputMessage.Content = "Continue."
		defaultUserMsg.UserInputMessage.ModelId = getKiroModelID(anthropicReq.Model)
		defaultUserMsg.UserInputMessage.Origin = "AI_EDITOR"

		lastRole := ""
		if len(history) > 0 {
			lastRole = "assistant" // the system-array block above ends on an assistant turn
		}
		emit := func(role string, entry any) {
			if lastRole == role {
				// Keep user/assistant strictly alternating.
				if role == "user" {
					history = append(history, assistantDefaultMsg)
				} else {
					history = append(history, defaultUserMsg)
				}
			}
			history = append(history, entry)
			lastRole = role
		}

		// All messages except the last one (the last is the current message).
		for i := 0; i < len(anthropicReq.Messages)-1; i++ {
			msg := anthropicReq.Messages[i]

			if msg.Role == "assistant" {
				assistantMsg := HistoryAssistantMessage{}
				assistantMsg.AssistantResponseMessage.Content = sanitizeHistoryContent(getMessageContent(msg.Content))

				toolUses := getMessageToolUses(msg.Content)
				if len(toolUses) > 0 {
					tuAny := make([]any, len(toolUses))
					for j, tu := range toolUses {
						tuAny[j] = tu
					}
					assistantMsg.AssistantResponseMessage.ToolUses = tuAny
				} else {
					assistantMsg.AssistantResponseMessage.ToolUses = make([]any, 0)
				}
				emit("assistant", assistantMsg)
				continue
			}

			// "user", "system", or any other role -> treated as a user turn.
			userMsg := HistoryUserMessage{}
			userMsg.UserInputMessage.Content = sanitizeHistoryContent(getMessageContent(msg.Content))
			userMsg.UserInputMessage.ModelId = getKiroModelID(anthropicReq.Model)
			userMsg.UserInputMessage.Origin = "AI_EDITOR"

			if toolResults := getMessageToolResults(msg.Content); len(toolResults) > 0 {
				userMsg.UserInputMessage.UserInputMessageContext = &HistoryUserContext{
					ToolResults: toolResults,
				}
			}
			emit("user", userMsg)
		}

		cwReq.ConversationState.History = history
	}

	return cwReq
}

func main() {
	// Keep the installed /models slash command in sync whenever the live model
	// set changes (fires on the first fetch and on every subsequent change).
	modelCatalog.SetOnChange(updateModelsDoc)

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
		// Refresh token on startup if expired
		if tok, err := getToken(); err == nil && tok.ExpiresAt != "" {
			if expiresAt, err := time.Parse(time.RFC3339, tok.ExpiresAt); err == nil && time.Until(expiresAt) < 5*time.Minute {
				fmt.Fprintf(os.Stderr, "Token expired or expiring soon, refreshing...\n")
				if err := tryRefreshToken(); err != nil {
					fmt.Fprintf(os.Stderr, "Token refresh failed: %v\nRun 'claude2kiro login' to re-authenticate.\n", err)
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "Token refreshed successfully.\n")
			}
		}
		// Use logged server (same handlers as TUI) for observability
		cfg := config.Get()
		srvLg := logger.NewLogger(cfg.Logging.MaxEntries)
		if cfg.Logging.Enabled {
			logDir := config.ExpandPath(cfg.Logging.Directory)
			if err := srvLg.EnableFileLogging(logDir); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to enable file logging: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "Logging to %s\n", logDir)
			}
		}
		startServerWithLogger(port, srvLg)
	case "migrate-logs":
		dateFilter := ""
		if len(os.Args) > 2 {
			dateFilter = os.Args[2]
		}
		if err := cmd.MigrateLogs(dateFilter); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	case "remote":
		remoteConnect()
	case "test":
		testProxy()
	case "models":
		printModels()
	case "resolve":
		printResolve()
	case "update":
		selfUpdate()
	case "logout":
		logout()
	case "credits":
		// `credits --web` opens the live auto-refreshing dashboard in a browser
		// instead of printing a one-shot snapshot.
		if len(os.Args) > 2 && (os.Args[2] == "--web" || os.Args[2] == "-w") {
			openCreditsDashboard()
			return
		}
		info := cmd.GetCreditsInfo()
		if info.Error != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", info.Error)
			os.Exit(1)
		}
		pct := 0.0
		if info.CreditsLimit > 0 {
			pct = info.CreditsUsed / info.CreditsLimit * 100
		}
		fmt.Printf("Plan:      %s\n", info.SubscriptionName)
		fmt.Printf("Used:      %.1f / %.0f (%.0f%%)\n", info.CreditsUsed, info.CreditsLimit, pct)
		fmt.Printf("Remaining: %.1f\n", info.CreditsRemaining)
		fmt.Printf("Resets in: %d days\n", info.DaysUntilReset)
	case "desktop":
		launchDesktop()
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

// injectKiroChangelog writes Kiro proxy info to Claude Code's changelog cache.
// Claude Code reads ~/.claude/cache/changelog.md and shows entries for versions
// newer than lastReleaseNotesSeen. We prepend a section with the CURRENT Claude
// Code version so it displays once on first Kiro session, then Claude Code
// updates lastReleaseNotesSeen and it won't show again until next CC update.
//
//go:embed plugin/.claude-plugin/plugin.json plugin/commands/*.md .claude-plugin/marketplace.json
var pluginFS embed.FS

// installPlugin installs or updates the kiro-proxy Claude Code plugin.
// It creates a local marketplace for claude2kiro, copies the files there,
// and registers the plugin globally so Claude Code loads it.
func installPlugin() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	marketplaceDir := filepath.Join(homeDir, ".claude", "plugins", "marketplaces", "claude2kiro")
	pluginSourceDir := filepath.Join(marketplaceDir, "kiro-proxy")
	cacheDir := filepath.Join(homeDir, ".claude", "plugins", "cache", "claude2kiro", "kiro-proxy", menu.Version)

	// Write plugin files to both marketplace and cache directories
	for _, dir := range []string{pluginSourceDir, cacheDir} {
		os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0755)
		os.MkdirAll(filepath.Join(dir, "commands"), 0755)

		entries, err := pluginFS.ReadDir("plugin/commands")
		if err != nil {
			continue
		}

		// Copy plugin.json
		if data, err := pluginFS.ReadFile("plugin/.claude-plugin/plugin.json"); err == nil {
			os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), data, 0644)
		}

		// Copy marketplace.json to the marketplace root
		if dir == pluginSourceDir {
			if data, err := pluginFS.ReadFile(".claude-plugin/marketplace.json"); err == nil {
				os.MkdirAll(filepath.Join(marketplaceDir, ".claude-plugin"), 0755)
				os.WriteFile(filepath.Join(marketplaceDir, ".claude-plugin", "marketplace.json"), data, 0644)
			}
		}

		// Copy command files
		for _, e := range entries {
			if data, err := pluginFS.ReadFile("plugin/commands/" + e.Name()); err == nil {
				os.WriteFile(filepath.Join(dir, "commands", e.Name()), data, 0644)
			}
		}
	}

	// Register marketplace in known_marketplaces.json
	marketplacesPath := filepath.Join(homeDir, ".claude", "plugins", "known_marketplaces.json")
	var marketplaces map[string]any
	if data, err := os.ReadFile(marketplacesPath); err == nil {
		json.Unmarshal(data, &marketplaces)
	}
	if marketplaces == nil {
		marketplaces = map[string]any{}
	}
	marketplaces["claude2kiro"] = map[string]any{
		"source": map[string]string{
			"source": "directory",
			"path":   marketplaceDir,
		},
		"installLocation": marketplaceDir,
		"lastUpdated":     time.Now().Format(time.RFC3339),
	}
	if data, err := json.MarshalIndent(marketplaces, "", "  "); err == nil {
		os.WriteFile(marketplacesPath, data, 0644)
	}

	// Register in installed_plugins.json
	installedPath := filepath.Join(homeDir, ".claude", "plugins", "installed_plugins.json")
	var installed map[string]any
	if data, err := os.ReadFile(installedPath); err == nil {
		json.Unmarshal(data, &installed)
	}
	if installed == nil {
		installed = map[string]any{"version": 2, "plugins": map[string]any{}}
	}
	plugins, _ := installed["plugins"].(map[string]any)
	if plugins == nil {
		plugins = map[string]any{}
	}

	pluginID := "kiro-proxy@claude2kiro"
	plugins[pluginID] = []any{map[string]any{
		"scope":       "user",
		"installPath": cacheDir,
		"version":     menu.Version,
		"installedAt": time.Now().Format(time.RFC3339),
		"lastUpdated": time.Now().Format(time.RFC3339),
	}}
	installed["plugins"] = plugins
	if data, err := json.MarshalIndent(installed, "", "  "); err == nil {
		os.WriteFile(installedPath, data, 0644)
	}

	// Enable in settings.json
	settingsPath := filepath.Join(homeDir, ".claude", "settings.json")
	var settings map[string]any
	if data, err := os.ReadFile(settingsPath); err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings != nil {
		enabledPlugins, _ := settings["enabledPlugins"].(map[string]any)
		if enabledPlugins != nil {
			enabledPlugins[pluginID] = true
			settings["enabledPlugins"] = enabledPlugins
			if data, err := json.MarshalIndent(settings, "", "  "); err == nil {
				os.WriteFile(settingsPath, data, 0644)
			}
		}
	}
}

// patchSemgrepHooks removes the synchronous UserPromptSubmit hook from the
// semgrep plugin that runs `semgrep mcp -k inject-secure-defaults-short` on
// every single prompt, adding 2-5 seconds of latency (Python + Datadog init).
// The security guidance is already injected at SessionStart via inject-secure-defaults.
// This patch is re-applied on each launch since plugin updates overwrite hooks.json.
func patchSemgrepHooks() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	// Find semgrep plugin hooks.json in cache
	semgrepDir := filepath.Join(homeDir, ".claude", "plugins", "cache", "claude-plugins-official", "semgrep")
	entries, err := os.ReadDir(semgrepDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		hooksPath := filepath.Join(semgrepDir, entry.Name(), "hooks", "hooks.json")
		data, err := os.ReadFile(hooksPath)
		if err != nil {
			continue
		}

		var hooks map[string]any
		if err := json.Unmarshal(data, &hooks); err != nil {
			continue
		}

		hooksObj, _ := hooks["hooks"].(map[string]any)
		if hooksObj == nil {
			continue
		}

		// Remove UserPromptSubmit if present
		if _, exists := hooksObj["UserPromptSubmit"]; exists {
			delete(hooksObj, "UserPromptSubmit")
			if patched, err := json.MarshalIndent(hooks, "", "  "); err == nil {
				os.WriteFile(hooksPath, patched, 0644)
			}
		}
	}
}

func injectKiroChangelog() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return
	}

	cacheDir := filepath.Join(homeDir, ".claude", "cache")
	os.MkdirAll(cacheDir, 0755)
	cachePath := filepath.Join(cacheDir, "changelog.md")

	// Detect Claude Code version from the binary
	ccVersion := ""
	if out, err := exec.Command("claude", "--version").Output(); err == nil {
		ccVersion = strings.TrimSpace(string(out))
	}
	if ccVersion == "" {
		return // can't determine version
	}

	kiroHeader := fmt.Sprintf("## %s", ccVersion)
	kiroEntry := fmt.Sprintf(`%s
- This session is powered by Kiro via claude2kiro proxy
- Credits from your Kiro subscription (not Anthropic API billing)
- Proxy source: github.com/sgeraldes/claude2kiro
`, kiroHeader)

	// Read existing changelog if any
	existing, _ := os.ReadFile(cachePath)

	// Don't inject if our entry is already there
	if strings.Contains(string(existing), "claude2kiro proxy") {
		return
	}

	// Prepend our entry to the changelog
	combined := kiroEntry + "\n" + string(existing)
	os.WriteFile(cachePath, []byte(combined), 0644)

	// Clear lastReleaseNotesSeen so Claude Code shows the notes
	claudePath := filepath.Join(homeDir, ".claude.json")
	var cfg map[string]any
	if data, err := os.ReadFile(claudePath); err == nil {
		json.Unmarshal(data, &cfg)
	}
	if cfg != nil {
		cfg["lastReleaseNotesSeen"] = ""
		if data, err := json.MarshalIndent(cfg, "", "  "); err == nil {
			os.WriteFile(claudePath, data, 0600)
		}
	}
}

// selfUpdate downloads the latest release from GitHub and replaces the current binary.
func selfUpdate() {
	currentVersion := menu.Version
	fmt.Printf("Current version: %s\n", currentVersion)
	fmt.Println("Checking for updates...")

	// Get latest release info from GitHub API
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", "https://api.github.com/repos/sgeraldes/claude2kiro/releases/latest", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to check for updates: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		fmt.Println("No releases found. Visit https://github.com/sgeraldes/claude2kiro/releases")
		return
	}

	body, _ := io.ReadAll(resp.Body)
	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(body, &release); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to parse release info: %v\n", err)
		os.Exit(1)
	}

	latestVersion := strings.TrimPrefix(release.TagName, "v")
	if latestVersion == currentVersion {
		fmt.Printf("Already up to date (v%s)\n", currentVersion)
		return
	}

	fmt.Printf("New version available: v%s -> v%s\n", currentVersion, latestVersion)

	// Determine platform asset name
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}
	assetName := fmt.Sprintf("claude2kiro-%s-%s%s", goos, goarch, ext)

	// Find matching asset
	var downloadURL string
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			downloadURL = asset.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		fmt.Fprintf(os.Stderr, "No binary found for %s/%s in release %s\n", goos, goarch, release.TagName)
		fmt.Fprintf(os.Stderr, "Available assets:\n")
		for _, a := range release.Assets {
			fmt.Fprintf(os.Stderr, "  - %s\n", a.Name)
		}
		os.Exit(1)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	// Download new binary
	dlResp, err := http.Get(downloadURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Download failed: %v\n", err)
		os.Exit(1)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Download failed: HTTP %d\n", dlResp.StatusCode)
		os.Exit(1)
	}

	newBinary, err := io.ReadAll(dlResp.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to read download: %v\n", err)
		os.Exit(1)
	}

	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to determine executable path: %v\n", err)
		os.Exit(1)
	}
	execPath, _ = filepath.EvalSymlinks(execPath)

	// On Windows, rename current exe first (can't overwrite running binary)
	oldPath := execPath + ".old"
	if goos == "windows" {
		os.Remove(oldPath) // Remove any previous .old file
		if err := os.Rename(execPath, oldPath); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to rename current binary: %v\n", err)
			fmt.Fprintf(os.Stderr, "Try closing all claude2kiro processes first.\n")
			os.Exit(1)
		}
	}

	// Write new binary
	if err := os.WriteFile(execPath, newBinary, 0755); err != nil {
		// On Windows, restore the old binary if write fails
		if goos == "windows" {
			os.Rename(oldPath, execPath)
		}
		fmt.Fprintf(os.Stderr, "Failed to write new binary: %v\n", err)
		os.Exit(1)
	}

	// Clean up old binary (best effort)
	if goos == "windows" {
		os.Remove(oldPath)
	}

	fmt.Printf("Updated to v%s\n", latestVersion)
}

// proxyPortFilePath returns ~/.claude2kiro/proxy.port
func proxyPortFilePath() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".claude2kiro", "proxy.port")
}

// writeProxyPortFile writes the proxy port to a well-known file
// so `claude2kiro remote` can discover the running proxy.
func writeProxyPortFile(port string) {
	path := proxyPortFilePath()
	os.MkdirAll(filepath.Dir(path), 0755)
	os.WriteFile(path, []byte(port), 0644)
}

// removeProxyPortFile cleans up the port file on shutdown.
func removeProxyPortFile() {
	os.Remove(proxyPortFilePath())
}

// readProxyPort reads the port from the proxy port file.
func readProxyPort() (string, error) {
	data, err := os.ReadFile(proxyPortFilePath())
	if err != nil {
		return "", fmt.Errorf("no running proxy found (no ~/.claude2kiro/proxy.port)")
	}
	port := strings.TrimSpace(string(data))
	if port == "" {
		return "", fmt.Errorf("proxy port file is empty")
	}
	return port, nil
}

// remoteConnect launches Claude Code connected to an already-running proxy.
// Usage: claude2kiro remote [claude args...]
// Reads the proxy port from ~/.claude2kiro/proxy.port (written by TUI/server mode).
func remoteConnect() {
	port, err := readProxyPort()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Start the proxy first with: claude2kiro (TUI) or claude2kiro server\n")
		os.Exit(1)
	}

	// Verify proxy is reachable
	baseURL := fmt.Sprintf("http://127.0.0.1:%s", port)
	resp, err := http.Get(baseURL + "/health")
	if err != nil || resp.StatusCode != 200 {
		fmt.Fprintf(os.Stderr, "Error: Proxy at %s is not responding\n", baseURL)
		fmt.Fprintf(os.Stderr, "Start the proxy first with: claude2kiro (TUI) or claude2kiro server\n")
		os.Exit(1)
	}
	resp.Body.Close()

	fmt.Printf("Connecting to proxy at %s\n", baseURL)

	// Pre-approve API key, install plugin, optimize hooks
	ensureApiKeyApproved()
	installPlugin()
	patchSemgrepHooks()

	// Build claude args
	claudeArgs := os.Args[2:] // everything after "remote"

	cfg := config.Get()
	if cfg.Advanced.SkipPermissions {
		hasPermFlag := false
		for _, arg := range claudeArgs {
			if strings.Contains(arg, "permission") || strings.Contains(arg, "allowedTools") {
				hasPermFlag = true
				break
			}
		}
		if !hasPermFlag {
			claudeArgs = append([]string{"--dangerously-skip-permissions"}, claudeArgs...)
		}
	}

	// Launch claude pointing at the proxy
	argv := append([]string{"claude"}, claudeArgs...)
	attr := &os.ProcAttr{
		Env: append(os.Environ(),
			"ANTHROPIC_BASE_URL="+baseURL,
			"ANTHROPIC_AUTH_TOKEN=claude2kiro",
			"CLAUDE2KIRO=1",
			"CLAUDE_CODE_DISABLE_THINKING=1",
		),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	}

	claudePath, err := exec.LookPath("claude")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: 'claude' not found in PATH\n")
		os.Exit(1)
	}

	fmt.Printf("Launching: claude %s\n", strings.Join(claudeArgs, " "))
	proc, err := os.StartProcess(claudePath, argv, attr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start claude: %v\n", err)
		os.Exit(1)
	}
	state, _ := proc.Wait()
	os.Exit(state.ExitCode())
}

// runClaudeWithProxy starts the proxy in-process, launches claude with env vars, and shuts down when claude exits.
// Usage: claude2kiro run [claude args...]
// Only minimal change to ~/.claude.json: pre-approves the proxy API key to skip the confirmation prompt.
func runClaudeWithProxy() {
	// 1. Verify we have a valid token, refresh if expired
	token, err := getToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "No token found. Run 'claude2kiro login' first.\n")
		os.Exit(1)
	}

	// Check if token is expired or expiring within 5 minutes, refresh before starting
	if token.ExpiresAt != "" {
		if expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt); err == nil {
			if time.Until(expiresAt) < 5*time.Minute {
				fmt.Fprintf(os.Stderr, "Token expired or expiring soon, refreshing...\n")
				if err := tryRefreshToken(); err != nil {
					fmt.Fprintf(os.Stderr, "Token refresh failed: %v\nRun 'claude2kiro login' to re-authenticate.\n", err)
					os.Exit(1)
				}
				// Re-read the refreshed token
				token, err = getToken()
				if err != nil {
					fmt.Fprintf(os.Stderr, "Failed to read refreshed token: %v\n", err)
					os.Exit(1)
				}
				fmt.Fprintf(os.Stderr, "Token refreshed successfully (expires: %s)\n", token.ExpiresAt)
			}
		}
	}

	// 1b. Pre-approve the proxy API key so Claude doesn't show the confirmation prompt
	ensureApiKeyApproved()

	// 1c. Install/update kiro-proxy Claude Code plugin and optimize hooks
	installPlugin()
	patchSemgrepHooks()

	// 1d. Inject Kiro release notes into Claude Code's changelog cache
	injectKiroChangelog()

	// 2. Create logger with file logging (same as TUI mode)
	cfg := config.Get()
	lg := logger.NewLogger(cfg.Logging.MaxEntries)

	if cfg.Logging.Enabled {
		logDir := config.ExpandPath(cfg.Logging.Directory)
		if err := lg.EnableFileLogging(logDir); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to enable file logging: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "Logging to %s\n", logDir)
		}
	}
	defer lg.DisableFileLogging()

	// 3. Listen on a random available port
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start proxy listener: %v\n", err)
		os.Exit(1)
	}
	port := listener.Addr().(*net.TCPAddr).Port

	// 4. Build the proxy HTTP server using the logged handler (same as TUI)
	mux := buildServerMux(lg)
	server := &http.Server{Handler: mux}

	// 5. Start proxy in background goroutine
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "Proxy server error: %v\n", err)
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	fmt.Printf("Proxy listening on %s\n", baseURL)

	// Advertise the chosen (random) port so slash commands like /credits and
	// /status can find this proxy. `run` picks an ephemeral port, so without
	// this the port file would hold a stale value from a previous server run.
	// Note: this function ends in os.Exit on error paths, which skips defers,
	// so the file is also removed explicitly before each os.Exit below.
	writeProxyPortFile(fmt.Sprintf("%d", port))
	defer removeProxyPortFile()

	// 6. Build claude command with remaining args
	claudeArgs := os.Args[2:] // everything after "run"

	// Prepend --dangerously-skip-permissions if configured (default: true)
	if cfg.Advanced.SkipPermissions {
		// Only add if user hasn't already passed a permission flag
		hasPermFlag := false
		for _, arg := range claudeArgs {
			if strings.Contains(arg, "permission") || strings.Contains(arg, "allowedTools") {
				hasPermFlag = true
				break
			}
		}
		if !hasPermFlag {
			claudeArgs = append([]string{"--dangerously-skip-permissions"}, claudeArgs...)
		}
	}

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

	// 7. Run claude (blocks until it exits)
	fmt.Printf("Launching: claude %s\n", strings.Join(claudeArgs, " "))
	runErr := claudeCmd.Run()

	// 8. Shutdown proxy gracefully
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	server.Shutdown(shutdownCtx)

	fmt.Println("Proxy stopped.")

	if runErr != nil {
		removeProxyPortFile() // os.Exit skips the deferred cleanup
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

	// Refresh an expired (or about-to-expire) token first, same as `run` does,
	// so a stale token doesn't masquerade as a backend failure.
	if err := refreshTokenIfStale(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Token refresh failed: %v\nRun 'claude2kiro login' to re-authenticate.\n", err)
		os.Exit(1)
	}

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
		// SSEEvent has .Event (string) and .Data (any)
		dataJSON, _ := json.Marshal(evt.Data)
		dataStr := string(dataJSON)
		if len(dataStr) > 300 {
			dataStr = dataStr[:300] + "..."
		}
		fmt.Printf("[%d] event=%s data=%s\n", i, evt.Event, dataStr)

		// Extract text from content_block_delta events
		if evt.Event == "content_block_delta" {
			if dataMap, ok := evt.Data.(map[string]any); ok {
				if delta, ok := dataMap["delta"].(map[string]any); ok {
					if text, ok := delta["text"].(string); ok {
						fullText += text
					}
				}
			}
		}
		// Also try assistantResponseEvent format (raw Kiro events before conversion)
		if dataMap, ok := evt.Data.(map[string]any); ok {
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
	fmt.Println("  claude2kiro desktop             - Ensure proxy is up + launch Claude Desktop (Windows)")
	fmt.Println("  claude2kiro remote [args...]     - Connect to running proxy (start TUI first)")
	fmt.Println("  claude2kiro update              - Self-update to latest GitHub release")
	fmt.Println("  claude2kiro test [msg] [model]  - Send test request to Kiro backend (debug tool)")
	fmt.Println("  claude2kiro claude              - Configure Claude Code settings (global)")
	fmt.Println("  claude2kiro server [port]       - Start Anthropic API proxy server (headless)")
	fmt.Println("  claude2kiro credits [--web]     - Show Kiro credit usage (--web opens live dashboard)")
	fmt.Println("  claude2kiro models              - List models available via Kiro (live)")
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
		StopServer: func() tea.Msg {
			activeTUIServerMu.Lock()
			srv := activeTUIServer
			activeTUIServerMu.Unlock()
			if srv != nil {
				ctx, cancel := context.WithTimeout(context.Background(), config.Get().Server.ShutdownTimeout)
				defer cancel()
				srv.Shutdown(ctx)
			}
			return nil
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
	Data map[string]any
}

// parseAnthropicSSE parses Anthropic SSE response into structured events
func parseAnthropicSSE(body []byte) []AnthropicSSEEvent {
	var events []AnthropicSSEEvent
	lines := strings.Split(string(body), "\n")
	var currentEvent, currentData string

	for _, line := range lines {
		if after, ok := strings.CutPrefix(line, "event: "); ok {
			currentEvent = after
		} else if after, ok := strings.CutPrefix(line, "data: "); ok {
			currentData = after
		} else if line == "" && currentEvent != "" && currentData != "" {
			var data map[string]any
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

	for i := range lines {
		line := lines[i]
		// Look for event: content_block_delta
		if strings.HasPrefix(line, "event: content_block_delta") {
			// Next line should be "data: {...}"
			if i+1 < len(lines) && strings.HasPrefix(lines[i+1], "data: ") {
				dataJSON := strings.TrimPrefix(lines[i+1], "data: ")
				var data map[string]any
				if err := jsonStr.Unmarshal([]byte(dataJSON), &data); err == nil {
					if delta, ok := data["delta"].(map[string]any); ok {
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
func forwardToAnthropic(originalReq *http.Request, body []byte) (*http.Response, error) {
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
	return proxyHttpClient.Do(req)
}

// forwardToAnthropicWithHeaders forwards a request to Anthropic API using pre-copied headers
// Used by comparison mode goroutines to avoid race conditions with request reuse
func forwardToAnthropicWithHeaders(headers http.Header, body []byte) (*http.Response, error) {
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
	return proxyHttpClient.Do(req)
}

// buildServerMux creates the HTTP mux with all endpoints using the logger.
// Shared by TUI mode (startServerWithLogger) and run mode (runClaudeWithProxy).
func buildServerMux(lg *logger.Logger) *http.ServeMux {
	mux := http.NewServeMux()

	// Warm the model catalog in the background so the first request doesn't pay
	// the ListAvailableModels fetch latency. This also fires the OnChange hook,
	// which refreshes the installed /models slash command if the model set changed.
	go modelCatalog.Warm()

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
			resp, err := forwardToAnthropic(r, body)
			if err != nil {
				lg.LogError(fmt.Sprintf("[ANTHROPIC DIRECT] Forward failed: %v", err))
				http.Error(w, fmt.Sprintf("Anthropic forward failed: %v", err), http.StatusBadGateway)
				return
			}
			defer resp.Body.Close()

			// Copy ALL response headers
			maps.Copy(w.Header(), resp.Header)
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
						if delta, ok := evt.Data["delta"].(map[string]any); ok {
							if text, ok := delta["text"].(string); ok {
								textParts = append(textParts, text)
							}
						}
					}
				}

				// Forward events to client (JSON/SSE API proxy, not HTML)
				flusher, ok := w.(http.Flusher)
				if ok {
					io.Copy(w, bytes.NewReader(respBody))
					flusher.Flush()
				} else {
					io.Copy(w, bytes.NewReader(respBody))
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
				io.Copy(w, bytes.NewReader(respBody))

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
			maps.Copy(headersCopy, r.Header)
			// Capture IDs for correlation
			compSessionID := sessionID
			compRequestID := reqResult.RequestID
			// Anthropic request runs in background goroutine
			go func(headers http.Header, sid, rid, ts string) {
				lg.LogComparison(sid, rid, "Sending parallel request to Anthropic")
				resp, err := forwardToAnthropicWithHeaders(headers, body)
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
				if cfg.Advanced.DebugMode {
					debugFile, err := debug.WriteDebugFileWithScrub(fmt.Sprintf("comparison-%s-anthropic", rid), respBody)
					if err != nil {
						lg.LogComparison(sid, rid, fmt.Sprintf("Failed to save debug file: %v", err))
						return
					}
					lg.LogComparison(sid, rid, fmt.Sprintf("Anthropic: preview=%q (%d bytes) -> %s", preview, len(respBody), debugFile))
				} else {
					lg.LogComparison(sid, rid, fmt.Sprintf("Anthropic: preview=%q (%d bytes)", preview, len(respBody)))
				}
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
				fmt.Fprintf(&sseBuffer, "event: %s\n", event.Event)
				fmt.Fprintf(&sseBuffer, "data: %s\n\n", event.Data)
			}

			if cfg.Advanced.DebugMode {
				debugFile, err := debug.WriteDebugFile(fmt.Sprintf("comparison-%s-kiro", reqResult.RequestID), []byte(sseBuffer.String()))
				if err != nil {
					lg.LogComparison(sessionID, reqResult.RequestID, fmt.Sprintf("Failed to save Kiro events: %v", err))
				} else {
					lg.LogComparison(sessionID, reqResult.RequestID, fmt.Sprintf("Kiro: %d events (%d bytes) -> %s", len(capturedKiroEvents), sseBuffer.Len(), debugFile))
				}
			} else {
				lg.LogComparison(sessionID, reqResult.RequestID, fmt.Sprintf("Kiro: %d events (%d bytes)", len(capturedKiroEvents), sseBuffer.Len()))
			}
		}
	})

	// Model discovery endpoint (used by Claude Desktop's gateway model picker,
	// which GETs /v1/models at launch to auto-populate the model list). Serves
	// the live Kiro catalog in Anthropic Models API shape. The `id` of each
	// entry is the Kiro backend model ID, so a model the picker sends back on a
	// request round-trips through getKiroModelID unchanged.
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		// Answer CORS preflight: Claude Desktop is an Electron/Chromium app and
		// may preflight the cross-origin discovery fetch before the real GET.
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			io.Copy(w, strings.NewReader(`{"type":"error","error":{"type":"invalid_request_error","message":"Only GET is supported"}}`))
			return
		}
		list := modelCatalog.Models()
		if len(list) == 0 {
			// Catalog unreachable (e.g. offline / not yet warmed). Fall back to
			// the curated static map so discovery still returns Claude IDs.
			list = staticModelList()
		}
		io.Copy(w, strings.NewReader(models.RenderModelsAPI(list)))
	})

	// Add credits endpoint (used by statusline scripts)
	mux.HandleFunc("/credits", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		info := cmd.GetCreditsInfo()
		if info.Error != nil {
			w.WriteHeader(http.StatusInternalServerError)
			errJSON, _ := json.Marshal(info.Error.Error())
			io.Copy(w, strings.NewReader(fmt.Sprintf(`{"error":%s}`, errJSON)))
			return
		}
		io.Copy(w, strings.NewReader(fmt.Sprintf(
			`{"used":%.1f,"limit":%.0f,"remaining":%.1f,"days_until_reset":%d,"plan":"%s"}`,
			info.CreditsUsed, info.CreditsLimit, info.CreditsRemaining, info.DaysUntilReset, info.SubscriptionName)))
	})

	// Add health check endpoint
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.Copy(w, strings.NewReader("OK"))
	})

	// Resolve endpoint: shows how a model id routes to Kiro and whether the
	// resolved model is in this account's live catalog. Used by the kiro-proxy
	// plugin to answer "which model am I actually on, and can I use it?".
	mux.HandleFunc("/resolve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		model := r.URL.Query().Get("model")
		if model == "" {
			w.WriteHeader(http.StatusBadRequest)
			io.Copy(w, strings.NewReader(`{"error":"missing ?model=<id> query parameter"}`))
			return
		}
		data, err := json.Marshal(resolveModelInfo(model))
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			io.Copy(w, strings.NewReader(`{"error":"failed to serialize resolve info"}`))
			return
		}
		w.Write(data)
	})

	// Live web dashboard: an auto-refreshing page that polls /credits and
	// /v1/models. This is the real-time credit view Claude Desktop's own UI
	// doesn't provide (its banner is read-once for external writers, and the
	// "Plan Usage" button is Anthropic-subscription telemetry, not gateway-fed).
	// Open with `claude2kiro credits --web` or browse to http://localhost:<port>/dashboard.
	mux.HandleFunc("/dashboard", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.Copy(w, strings.NewReader(webdash.HTML()))
	})

	// Add 404 handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		lg.LogInfo("Unknown endpoint accessed: " + r.URL.Path)
		http.Error(w, "404 Not Found", http.StatusNotFound)
	})

	return mux
}

var (
	activeTUIServer   *http.Server
	activeTUIServerMu sync.Mutex
	proxyHttpClient   = &http.Client{} // Shared client for connection pooling
)

// startServerWithLogger starts the HTTP server with logging (used by TUI mode).
func startServerWithLogger(port string, lg *logger.Logger) {
	mux := buildServerMux(lg)

	// Write proxy port file so `claude2kiro remote` can find us
	writeProxyPortFile(port)
	defer removeProxyPortFile()

	// Localhost-only proxy; TLS not needed for loopback traffic.
	srv := &http.Server{Addr: ":" + port, Handler: mux}

	// Create listener to ensure port is bound before notifying TUI
	ln, err := net.Listen("tcp", ":"+port)
	if err != nil {
		lg.LogError(fmt.Sprintf("Server failed to start: %v", err))
		if p := lg.GetProgram(); p != nil {
			p.Send(dashboard.ServerStoppedMsg{Err: err})
		}
		return
	}

	activeTUIServerMu.Lock()
	activeTUIServer = srv
	activeTUIServerMu.Unlock()

	lg.LogInfo(fmt.Sprintf("Server started on port %s", port))

	// Notify TUI that server started
	if p := lg.GetProgram(); p != nil {
		p.Send(dashboard.ServerStartedMsg{Port: port})
	}

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		lg.LogError(fmt.Sprintf("Server error: %v", err))
	}

	activeTUIServerMu.Lock()
	activeTUIServer = nil
	activeTUIServerMu.Unlock()
	if p := lg.GetProgram(); p != nil {
		p.Send(dashboard.ServerStoppedMsg{Err: nil})
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
	cfg := config.Get()
	if cfg.Advanced.DebugMode {
		debug.WriteDebugFileWithScrub("cw-request", cwReqBody)
	}

	// Log the conversationId for tracing
	lg.LogInfo(fmt.Sprintf("Request convId=%s model=%s historyLen=%d", cwReq.ConversationState.ConversationId[:8], anthropicReq.Model, len(cwReq.ConversationState.History)))

	// Create streaming request
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
	getKiroSema() <- struct{}{}
	defer func() { <-getKiroSema() }()

	// Send request with retry logic for transient errors
	var resp *http.Response
	var lastErr error

	for attempt := range 3 {
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

		resp, lastErr = proxyHttpClient.Do(proxyReq)
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

		// A context-length-exceeded 400 is not transient (a smaller request is the
		// only fix) and must not be retried or mislabeled. Compute once, reuse below.
		contextTooLong := resp.StatusCode == 400 && isContextLengthExceeded(body)

		if resp.StatusCode == 400 && attempt < 2 && !contextTooLong {
			// Transient 400 error - retry.
			lg.LogInfo(fmt.Sprintf("400 error convId=%s attempt=%d reqSize=%d: %s", cwReq.ConversationState.ConversationId[:8], attempt+1, len(cwReqBody), string(body)))
			continue
		}

		// Non-retryable error or final attempt - save the failing request for debugging
		if cfg.Advanced.DebugMode {
			debug.WriteDebugFileWithScrub("cw-FAILED", cwReqBody)
		}
		lg.LogError(fmt.Sprintf("FINAL ERROR convId=%s status=%d: %s", cwReq.ConversationState.ConversationId[:8], resp.StatusCode, string(body)))

		if resp.StatusCode == 403 && isInvalidBearerToken(body) {
			if err := tryRefreshToken(); err != nil {
				lg.LogError(fmt.Sprintf("Token refresh failed: %v", err))
				sendErrorEvent(w, flusher, "error", fmt.Errorf("Token expired, refresh failed: %v. Please re-login", err))
			} else {
				lg.LogInfo("Token refreshed successfully")
				sendErrorEvent(w, flusher, "error", fmt.Errorf("Token refreshed, please retry"))
			}
		} else if contextTooLong {
			// The request exceeded Kiro's input-size limit. Retrying can't help,
			// and the generic path below would label it overloaded_error (which
			// the Anthropic SDK auto-retries), hiding the real cause behind a
			// generic "API Error". Surface it as non-retryable with a clear fix.
			sendNonRetryableErrorEvent(w, flusher, "Context too long: this conversation exceeds Kiro's input-size limit. "+
				"Run /clear to start fresh or /compact to shrink the history, then retry. "+
				"Trimming unused MCP servers also reduces per-request size.")
		} else if resp.StatusCode == 403 {
			// 403 with a valid token is access denial — most commonly a model
			// this Kiro account/plan does not expose. Send a non-retryable
			// error so Claude Code surfaces it instead of retrying forever.
			sendNonRetryableErrorEvent(w, flusher, fmt.Sprintf(
				"Kiro rejected model %q (resolved to %q): %s — this usually means the model is not available on your Kiro account/plan. Run 'claude2kiro models' to see your account's model list, then switch with /model <id>.",
				anthropicReq.Model, getKiroModelID(anthropicReq.Model), strings.TrimSpace(string(body))))
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
	if cfg.Advanced.DebugMode {
		debug.WriteDebugFile("cw-response", respBody)
	}

	// Use CodeWhisperer parser
	events := parser.ParseEvents(respBody)

	// Restore original tool names (sanitized for CodeWhisperer's 64-char limit)
	// so Claude Code recognizes its tools.
	restoreToolNames(events, buildToolNameMap(anthropicReq.Tools))
	var responseText strings.Builder

	// Debug: Log if no events parsed
	if len(events) == 0 {
		lg.LogError(fmt.Sprintf("WARNING: Parser returned 0 events from %d bytes response", len(respBody)))
	}

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
					"input_tokens":  len(cwReqBody) / 4, // Approximate from CW request body size
					"output_tokens": 0,
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

		// Separate events into text deltas and non-text (tool blocks, message_delta).
		// Anthropic requires text block to be fully closed before tool blocks begin.
		outputChars := 0
		var textEvents []parser.SSEEvent
		var toolEvents []parser.SSEEvent
		for _, e := range events {
			if e.Event == "content_block_delta" {
				if dataMap, ok := e.Data.(map[string]any); ok {
					if delta, ok := dataMap["delta"].(map[string]any); ok {
						if text, ok := delta["text"].(string); ok {
							outputChars += len(text)
							responseText.WriteString(text)
							textEvents = append(textEvents, e)
							continue
						}
						if pj, ok := delta["partial_json"].(string); ok {
							outputChars += len(pj)
						}
					}
				}
			}
			toolEvents = append(toolEvents, e)
		}

		delayMs := int(cfg.Network.StreamingDelayMax.Milliseconds())
		delayFn := func() {
			if delayMs > 0 {
				time.Sleep(time.Duration(cryptoRandIntn(delayMs)) * time.Millisecond)
			}
		}

		// Phase 1: Send text deltas
		for _, e := range textEvents {
			sendSSEEvent(w, flusher, e.Event, e.Data, capturedEvents)
			delayFn()
		}

		// Close text block before tool blocks (if we opened one)
		if hasTextContent || !hasToolUse {
			contentBlockStop := map[string]any{"index": 0, "type": "content_block_stop"}
			sendSSEEvent(w, flusher, "content_block_stop", contentBlockStop, capturedEvents)
		}

		// Phase 2: Send tool blocks and message_delta
		for _, e := range toolEvents {
			sendSSEEvent(w, flusher, e.Event, e.Data, capturedEvents)
			delayFn()
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
				"usage": map[string]any{"output_tokens": outputChars / 4},
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
	tokenMutex.Lock()
	cachedToken = nil
	tokenMutex.Unlock()

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
	kiroVersion      = "0.11.107" // Kiro IDE version to impersonate
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

// proxyBaseURL returns the local proxy base URL from configured port (default 8080).
func proxyBaseURL() string {
	port := config.Get().Server.Port
	if port == "" {
		port = "8080"
	}
	return "http://localhost:" + port
}

// proxyReachable reports whether a proxy is already answering /health at baseURL.
func proxyReachable(baseURL string) bool {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get(baseURL + "/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// openCreditsDashboard opens the live web dashboard, telling the user to start
// the proxy first if it isn't reachable. Triggered by `claude2kiro credits --web`.
func openCreditsDashboard() {
	base := proxyBaseURL()
	url := base + "/dashboard"
	if !proxyReachable(base) {
		fmt.Fprintf(os.Stderr, "Proxy not reachable at %s.\nStart it first with `claude2kiro server` (or `claude2kiro desktop`), then re-run.\n", base)
		fmt.Printf("Dashboard URL (once the proxy is up): %s\n", url)
		os.Exit(1)
	}
	fmt.Printf("Opening live dashboard: %s\n", url)
	if err := openBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "Could not open browser automatically: %v\nOpen this URL manually: %s\n", err, url)
		os.Exit(1)
	}
}

// claudeDesktopPIDs returns the PIDs of running Claude Desktop GUI processes
// (the Store app's Claude.exe), excluding the embedded claude-code CLI under
// Claude-3p so we never kill an active Code-tab session. Windows-only detail;
// returns nil elsewhere.
func claudeDesktopPIDs() []int {
	if runtime.GOOS != "windows" {
		return nil
	}
	// Match the Store GUI exe by its package path and explicitly exclude the
	// embedded claude-code CLI. The `-like '*WindowsApps*Claude*'` requires a
	// real path, so a null/inaccessible Path (which `-notlike` alone would let
	// through, since $null -notlike '*x*' is $true) can never land in the kill
	// list — we only ever target processes we've positively identified.
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`Get-Process claude -ErrorAction SilentlyContinue | Where-Object { $_.Path -like '*WindowsApps*Claude*' -and $_.Path -notlike '*claude-code*' } | Select-Object -ExpandProperty Id`).Output()
	if err != nil {
		return nil
	}
	var pids []int
	for line := range strings.FieldsSeq(string(out)) {
		if n, err := strconv.Atoi(strings.TrimSpace(line)); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

// launchDesktop ensures the proxy is up, optionally restarts Claude Desktop
// (prompting first if it's already running, since Desktop reads the gateway
// config only at launch), and launches the Store app. Triggered by
// `claude2kiro desktop`.
func launchDesktop() {
	if runtime.GOOS != "windows" {
		fmt.Fprintf(os.Stderr, "`desktop` currently supports Windows (Claude Desktop Store app) only.\n")
		os.Exit(1)
	}

	// 1. Require a valid token (same gate as `run`).
	if _, err := getToken(); err != nil {
		fmt.Fprintf(os.Stderr, "No token found. Run 'claude2kiro login' first.\n")
		os.Exit(1)
	}

	// 2. Ensure the proxy is reachable; Desktop's gateway config points at it.
	base := proxyBaseURL()
	if proxyReachable(base) {
		fmt.Printf("Proxy already running at %s.\n", base)
	} else {
		fmt.Printf("Proxy not running — starting it in the background...\n")
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Could not locate own binary: %v\n", err)
			os.Exit(1)
		}
		// Start the proxy on the same port proxyBaseURL()/the health check use,
		// so a non-default configured port doesn't cause a silent mismatch.
		port := config.Get().Server.Port
		if port == "" {
			port = "8080"
		}
		srv := exec.Command(exe, "server", port)
		srv.Stdout, srv.Stderr = nil, nil
		if err := srv.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start proxy: %v\n", err)
			os.Exit(1)
		}
		// Wait briefly for /health to come up.
		ok := false
		for range 20 {
			if proxyReachable(base) {
				ok = true
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if !ok {
			fmt.Fprintf(os.Stderr, "Proxy did not become healthy at %s in time.\n", base)
			os.Exit(1)
		}
		fmt.Printf("Proxy is up at %s (PID %d).\n", base, srv.Process.Pid)
	}

	// 3. If Claude Desktop is already running, prompt before killing it.
	//    A restart is how Desktop picks up gateway/model/config changes.
	if pids := claudeDesktopPIDs(); len(pids) > 0 {
		fmt.Printf("Claude Desktop is already running (PID %s).\n", joinInts(pids))
		fmt.Print("Restart it so config changes take effect? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans == "y" || ans == "yes" {
			for _, pid := range pids {
				_ = exec.Command("powershell", "-NoProfile", "-Command",
					fmt.Sprintf("Stop-Process -Id %d -Force", pid)).Run()
			}
			fmt.Println("Stopped existing Claude Desktop. Relaunching...")
			time.Sleep(1 * time.Second)
		} else {
			fmt.Println("Leaving the running instance as-is. Nothing launched.")
			return
		}
	}

	// 4. Launch the Claude Desktop Store app via its AppUserModelId.
	const appID = `Claude_pzs8sxrjxfjjc!Claude`
	launch := exec.Command("explorer.exe", `shell:AppsFolder\`+appID)
	if err := launch.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to launch Claude Desktop: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Launched Claude Desktop.")
}

// joinInts formats a slice of ints as a comma-separated string.
func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = strconv.Itoa(n)
	}
	return strings.Join(parts, ", ")
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
	hash := sha256.Sum256(fmt.Appendf(nil, `{"startUrl":"%s"}`, startUrl))
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

	tokenMutex.Lock()
	cachedToken = nil
	tokenMutex.Unlock()

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

	var jsonData map[string]any

	err = jsonStr.Unmarshal(data, &jsonData)

	if err != nil {
		fmt.Printf("Failed to parse JSON file: %v\n", err)
		os.Exit(1)
	}

	jsonData["hasCompletedOnboarding"] = true
	jsonData["claude2kiro"] = true
	jsonData["oauthAccount"] = map[string]any{
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
var (
	cachedToken     *TokenData
	cachedTokenTime time.Time
	tokenMutex      sync.Mutex
)

func getToken() (TokenData, error) {
	tokenMutex.Lock()
	defer tokenMutex.Unlock()

	// Use cache if it's fresh (less than 1 minute old, to pick up manual file edits)
	if cachedToken != nil && time.Since(cachedTokenTime) < time.Minute {
		return *cachedToken, nil
	}

	tokenPath := getTokenFilePath()

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		return TokenData{}, fmt.Errorf("failed to read token file: %v", err)
	}

	var token TokenData
	if err := jsonStr.Unmarshal(data, &token); err != nil {
		return TokenData{}, fmt.Errorf("failed to parse token file: %v", err)
	}

	// Discover and persist the IdC/Enterprise profile ARN once. The Kiro backend
	// associates requests with a CodeWhisperer profile; for IdC users it must be
	// their own account-specific profile. We persist it so this runs only until
	// the token file has it.
	if token.AuthMethod == "IdC" && token.ProfileArn == "" {
		if arn := discoverProfileArn(token.AccessToken); arn != "" {
			token.ProfileArn = arn
			_ = saveToken(&token) // best-effort
		}
	}

	cachedToken = &token
	cachedTokenTime = time.Now()

	return token, nil
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
	resp, err := proxyHttpClient.Do(proxyReq)
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

	// Debug: save CW request/response only when KIRO_DEBUG is set
	if os.Getenv("KIRO_DEBUG") != "" {
		debugDir := filepath.Join(os.TempDir(), "kiro-debug")
		os.MkdirAll(debugDir, 0700)
		os.WriteFile(filepath.Join(debugDir, "last-cw-request.json"), cwReqBody, 0600)
		os.WriteFile(filepath.Join(debugDir, "last-cw-response.bin"), cwRespBody, 0600)
	}

	respBodyStr := string(cwRespBody)

	events := parser.ParseEvents(cwRespBody)

	// Restore original tool names (sanitized for CodeWhisperer's 64-char limit).
	restoreToolNames(events, buildToolNameMap(anthropicReq.Tools))

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

	io.Copy(w, strings.NewReader("event: "+eventType+"\n"))
	io.Copy(w, strings.NewReader("data: "+string(json)+"\n\n"))
	flusher.Flush()

}

// sendErrorEvent sends an error event
// isInvalidBearerToken reports whether a CodeWhisperer 403 body indicates an
// expired/invalid bearer token. Kiro also answers 403 (AccessDeniedException)
// for models an account cannot use, so a 403 alone must not trigger the
// token-refresh path — that misdiagnosis made unavailable models look like an
// endless "Token refreshed, please retry" loop.
func isInvalidBearerToken(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "bearer token") ||
		strings.Contains(s, "token is invalid") ||
		strings.Contains(s, "token expired") ||
		strings.Contains(s, "invalid_token")
}

// isContextLengthExceeded reports whether a CodeWhisperer 400 body indicates the
// request exceeded the backend's input-size limit. Retrying an oversized request
// can never succeed, and the default error path mislabels it as overloaded_error
// (which the Anthropic SDK auto-retries), so the client only ever sees a generic
// "API Error" instead of the real cause.
func isContextLengthExceeded(body []byte) bool {
	var parsed struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if strings.ToLower(parsed.Reason) == "content_length_exceeds_threshold" {
			return true
		}
		// Match the message field exactly rather than scanning the whole body:
		// AWS may echo user content (e.g. an oversized tool description) into
		// other 400 errors, and a loose substring scan would misfire on those.
		return strings.TrimRight(strings.ToLower(parsed.Message), ". ") == "input is too long"
	}
	// Non-JSON body: only trust the structured enum, never the English phrase.
	return strings.Contains(strings.ToLower(string(body)), "content_length_exceeds_threshold")
}

// sendNonRetryableErrorEvent emits an Anthropic error event typed
// invalid_request_error so the client shows it to the user. sendErrorEvent
// uses overloaded_error, which the Anthropic SDK auto-retries — correct for
// transient failures, wrong for permanent ones like an unavailable model.
func sendNonRetryableErrorEvent(w http.ResponseWriter, flusher http.Flusher, message string) {
	errorResp := map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "invalid_request_error",
			"message": message,
		},
	}
	sendSSEEvent(w, flusher, "error", errorResp, nil)
}

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
