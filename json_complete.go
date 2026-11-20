package main

import "strings"

// jsonCompletionSuffix returns the text that, appended to partial, makes it a
// complete JSON document. partial is the start of a document cut off anywhere
// (the input of a tool call the backend stopped sending). Nothing already in
// partial is changed, since the client has received it: an open string is
// closed, a half-written literal or number is finished, a key without a value
// gets null, and every open object and array is closed. An empty partial
// needs nothing (an empty tool input reads as {}), and neither does a
// complete document.
func jsonCompletionSuffix(partial string) string {
	if strings.TrimSpace(partial) == "" {
		return ""
	}

	// Where each open container is: an object waits for a key, is inside a
	// key, waits for its colon, waits for a value or has one; an array waits
	// for a value or has one. afterComma tells a key or value that must follow
	// a comma from an empty container.
	const (
		wantKey = iota
		wantColon
		wantValue
		haveValue
	)
	type frame struct {
		object     bool
		state      int
		afterComma bool
	}
	var stack []frame
	inString, inKey, escaped := false, false, false
	hexLeft := 0              // hex digits still owed by a \u escape
	var token strings.Builder // a literal or number being written

	endToken := func() {
		if token.Len() > 0 && len(stack) > 0 {
			stack[len(stack)-1].state = haveValue
		}
		token.Reset()
	}

	for i := 0; i < len(partial); i++ {
		c := partial[i]
		if inString {
			switch {
			case hexLeft > 0:
				hexLeft--
			case escaped:
				escaped = false
				if c == 'u' {
					hexLeft = 4
				}
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
				if len(stack) > 0 {
					top := &stack[len(stack)-1]
					if inKey {
						top.state = wantColon
					} else {
						top.state = haveValue
					}
				}
				inKey = false
			}
			continue
		}
		switch c {
		case ' ', '\t', '\n', '\r':
			endToken()
		case '{', '[':
			endToken()
			if len(stack) > 0 {
				stack[len(stack)-1].state = haveValue // the container is the parent's value
			}
			if c == '{' {
				stack = append(stack, frame{object: true, state: wantKey})
			} else {
				stack = append(stack, frame{state: wantValue})
			}
		case '}', ']':
			endToken()
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case ',':
			endToken()
			if len(stack) > 0 {
				top := &stack[len(stack)-1]
				if top.object {
					top.state = wantKey
				} else {
					top.state = wantValue
				}
				top.afterComma = true
			}
		case ':':
			endToken()
			if len(stack) > 0 {
				stack[len(stack)-1].state = wantValue
			}
		case '"':
			endToken()
			inString = true
			inKey = len(stack) > 0 && stack[len(stack)-1].object && stack[len(stack)-1].state == wantKey
		default:
			token.WriteByte(c)
		}
	}

	var suffix strings.Builder
	if inString {
		if hexLeft > 0 {
			suffix.WriteString(strings.Repeat("0", hexLeft))
		} else if escaped {
			suffix.WriteByte('\\') // the pending backslash becomes an escaped backslash
		}
		suffix.WriteByte('"')
		if len(stack) > 0 {
			if inKey {
				suffix.WriteString(":null")
			}
			stack[len(stack)-1].state = haveValue
		}
	} else if token.Len() > 0 {
		suffix.WriteString(completeToken(token.String()))
		if len(stack) > 0 {
			stack[len(stack)-1].state = haveValue
		}
	}

	for i := len(stack) - 1; i >= 0; i-- {
		f := stack[i]
		switch f.state {
		case wantKey:
			if f.afterComma {
				suffix.WriteString(`"":null`)
			}
		case wantColon:
			suffix.WriteString(":null")
		case wantValue:
			if f.object || f.afterComma {
				suffix.WriteString("null")
			}
		}
		if f.object {
			suffix.WriteByte('}')
		} else {
			suffix.WriteByte(']')
		}
	}
	return suffix.String()
}

// completeToken finishes a literal or number cut off after tok.
func completeToken(tok string) string {
	for _, lit := range []string{"true", "false", "null"} {
		if strings.HasPrefix(lit, tok) {
			return lit[len(tok):]
		}
	}
	switch tok[len(tok)-1] {
	case '-', '+', '.', 'e', 'E':
		return "0"
	}
	return ""
}
