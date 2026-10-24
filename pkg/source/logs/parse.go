// Package logs parses Bee log lines and classifies them into events.
//
// Bee's default (non-JSON) format renders every key and value as a pair:
//
//	"time"="2026-10-23 10:00:00.000000" "level"="debug" "logger"="node/pushsync" "v"=1 "msg"="chunk pushed" "chunk_address"="ab.." "peer_address"="cd.."
//
// Values are quoted strings, bare numbers/booleans/null, or JSON-like
// composites ({...} / [...]). Lines come from an untrusted stream (container
// output), so the parser never panics and bounds its work by line length.
package logs

import (
	"strconv"
	"strings"
	"time"

	"beescope/pkg/model"
)

// MaxLine is the longest line the parser inspects; longer lines are truncated
// and marked partial.
const MaxLine = 64 << 10

// timeLayout is Bee's default timestamp layout (pkg/log).
const timeLayout = "2006-01-02 15:04:05.000000"

// Parse parses one Bee log line. ok is false when the line is not in Bee's
// key/value format (e.g. a Go panic trace); the caller may still keep it raw.
func Parse(line string) (out model.LogLine, ok bool) {
	if len(line) > MaxLine {
		line = line[:MaxLine]
		out.Partial = true
	}
	out.Raw = line
	s := strings.TrimSpace(line)
	if s == "" || s[0] != '"' {
		return out, false
	}
	pairs := 0
	for len(s) > 0 {
		key, rest, okKey := readQuoted(s)
		if !okKey || len(rest) == 0 || rest[0] != '=' {
			break
		}
		rest = rest[1:]
		val, rest2, okVal := readValue(rest)
		if !okVal {
			break
		}
		pairs++
		assign(&out, key, val)
		s = strings.TrimLeft(rest2, " ")
	}
	if pairs == 0 || out.Msg == "" && out.Level == "" {
		return out, false
	}
	return out, true
}

func assign(out *model.LogLine, key, val string) {
	switch key {
	case "time":
		if t, err := time.Parse(timeLayout, val); err == nil {
			out.Time = t
		}
	case "level":
		out.Level = val
	case "logger":
		out.Logger = val
	case "v":
		out.V, _ = strconv.Atoi(val)
	case "msg":
		out.Msg = val
	case "error":
		out.Error = val
	case "caller":
		out.Caller = strings.TrimSpace(val)
	default:
		if out.Fields == nil {
			out.Fields = make(map[string]string, 4)
		}
		if len(out.Fields) < 64 {
			out.Fields[key] = val
		}
	}
}

// readQuoted reads a Go-quoted string at the start of s.
func readQuoted(s string) (val, rest string, ok bool) {
	if len(s) == 0 || s[0] != '"' {
		return "", s, false
	}
	esc := false
	for i := 1; i < len(s); i++ {
		c := s[i]
		switch {
		case esc:
			esc = false
		case c == '\\':
			esc = true
		case c == '"':
			raw := s[:i+1]
			if u, err := strconv.Unquote(raw); err == nil {
				return u, s[i+1:], true
			}
			return raw[1 : len(raw)-1], s[i+1:], true
		}
	}
	return "", s, false
}

// readValue reads one value: quoted, composite or bare.
func readValue(s string) (val, rest string, ok bool) {
	if len(s) == 0 {
		return "", s, true
	}
	switch s[0] {
	case '"':
		return readQuoted(s)
	case '{', '[':
		depth := 0
		inStr, esc := false, false
		for i := 0; i < len(s); i++ {
			c := s[i]
			if inStr {
				switch {
				case esc:
					esc = false
				case c == '\\':
					esc = true
				case c == '"':
					inStr = false
				}
				continue
			}
			switch c {
			case '"':
				inStr = true
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return s[:i+1], s[i+1:], true
				}
			}
		}
		return s, "", true // unterminated: take the rest
	default:
		i := strings.IndexByte(s, ' ')
		if i < 0 {
			return s, "", true
		}
		return s[:i], s[i:], true
	}
}

// SplitKubeTimestamp splits the RFC3339Nano prefix that the kube log API
// adds when timestamps=true.
func SplitKubeTimestamp(line string) (time.Time, string) {
	i := strings.IndexByte(line, ' ')
	if i <= 0 || i > 40 {
		return time.Time{}, line
	}
	t, err := time.Parse(time.RFC3339Nano, line[:i])
	if err != nil {
		return time.Time{}, line
	}
	return t, line[i+1:]
}
