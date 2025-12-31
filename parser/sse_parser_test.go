package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
)

func TestParseCodeWhispererEvents(t *testing.T) {
	data, err := os.ReadFile("response.raw")
	if err != nil {
		t.Skip("Skipping test: response.raw fixture not found. This test requires a captured response file.")
	}

	events := ParseEvents(data)

	if len(events) == 0 {
		t.Error("Expected at least one event from response.raw")
	}

	for _, e := range events {
		fmt.Printf("event: %s\n", e.Event)
		jsonData, _ := json.Marshal(e.Data)
		fmt.Printf("data: %s\n\n", string(jsonData))
	}
}
