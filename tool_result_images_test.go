package main

import (
	"testing"

	"github.com/sgeraldes/claude2kiro/internal/config"
)

func base64Image(mediaType, data string) map[string]any {
	return map[string]any{
		"type":   "image",
		"source": map[string]any{"type": "base64", "media_type": mediaType, "data": data},
	}
}

// An image inside a tool_result is forwarded like a top-level image of the
// current message, after the top-level ones; the result's text still travels
// in toolResults.
func TestImagesInsideAToolResultAreForwarded(t *testing.T) {
	withConfig(t, config.Default())
	req := AnthropicRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 64,
		Messages: []AnthropicRequestMessage{
			{Role: "user", Content: "take a screenshot"},
			{Role: "assistant", Content: []any{toolUseBlock("tu_img")}},
			{Role: "user", Content: []any{
				map[string]any{
					"type":        "tool_result",
					"tool_use_id": "tu_img",
					"content": []any{
						map[string]any{"type": "text", "text": "screenshot taken"},
						base64Image("image/jpeg", "SCREENSHOT"),
					},
				},
				base64Image("image/png", "TOPLEVEL"),
			}},
		},
	}
	cw := buildCodeWhispererRequest(req, TokenData{})
	msg := cw.ConversationState.CurrentMessage.UserInputMessage
	if len(msg.Images) != 2 {
		t.Fatalf("images = %+v, want the top-level image and the tool_result image", msg.Images)
	}
	if msg.Images[0].Source.Bytes != "TOPLEVEL" || msg.Images[0].Format != "png" {
		t.Fatalf("first image = %+v, want the top-level png", msg.Images[0])
	}
	if msg.Images[1].Source.Bytes != "SCREENSHOT" || msg.Images[1].Format != "jpeg" {
		t.Fatalf("second image = %+v, want the tool_result jpeg", msg.Images[1])
	}
	results := msg.UserInputMessageContext.ToolResults
	if len(results) != 1 || len(results[0].Content) != 1 || results[0].Content[0].Text != "screenshot taken" {
		t.Fatalf("toolResults = %+v, want the result's text only", results)
	}
}
