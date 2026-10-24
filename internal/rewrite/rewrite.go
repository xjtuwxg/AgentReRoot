// Package rewrite replaces absolute path prefixes inside file contents.
//
// Every replacement honours path boundaries: rewriting "/a/b" touches "/a/b"
// and "/a/b/c" but never "/a/bc" or "/x/a/b".
package rewrite

import (
	"bytes"
	"encoding/json"
	"strings"
)

// Text rewrites occurrences of the path old to new in plain text and returns
// the new content together with the number of replacements.
func Text(data []byte, old, new string) ([]byte, int) {
	return replace(data, []byte(old), []byte(new))
}

// JSON rewrites occurrences of old inside JSON (or TOML basic) strings. The
// paths are escaped the way a JSON encoder would write them before matching.
func JSON(data []byte, old, new string) ([]byte, int) {
	return replace(data, []byte(EscapeJSON(old)), []byte(EscapeJSON(new)))
}

// EscapeJSON returns s encoded as the inside of a JSON string literal.
func EscapeJSON(s string) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	b := bytes.TrimSpace(buf.Bytes())
	return string(b[1 : len(b)-1])
}

func replace(data, old, new []byte) ([]byte, int) {
	if len(old) == 0 || !bytes.Contains(data, old) {
		return data, 0
	}
	var out bytes.Buffer
	out.Grow(len(data))
	n := 0
	i := 0
	for {
		j := bytes.Index(data[i:], old)
		if j < 0 {
			break
		}
		start := i + j
		end := start + len(old)
		if startBoundary(data, start) && endBoundary(data, end) {
			out.Write(data[i:start])
			out.Write(new)
			n++
		} else {
			out.Write(data[i:end])
		}
		i = end
	}
	if n == 0 {
		return data, 0
	}
	out.Write(data[i:])
	return out.Bytes(), n
}

// pathByte reports whether c can be part of a path component.
func pathByte(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '.', c == '_', c == '-', c == '~', c == '/', c >= 0x80:
		return true
	}
	return false
}

func startBoundary(data []byte, i int) bool {
	if i == 0 {
		return true
	}
	prev := data[i-1]
	// "file:///Users/..." – the path is preceded by the URL's "//".
	if prev == '/' && i >= 2 && data[i-2] == '/' {
		return true
	}
	// An escape sequence such as "\n" in JSON text ends the previous token.
	if i >= 2 && data[i-2] == '\\' && strings.IndexByte("bfnrt", prev) >= 0 {
		return true
	}
	return !pathByte(prev)
}

func endBoundary(data []byte, i int) bool {
	if i >= len(data) {
		return true
	}
	next := data[i]
	if next == '/' {
		return true
	}
	// A trailing period ends a sentence ("see /a/b.") but "/a/b.txt" is a
	// different path.
	if next == '.' {
		return i+1 >= len(data) || !pathByte(data[i+1]) || data[i+1] == '/'
	}
	return !pathByte(next)
}
