package docutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// CanonicalJSON returns deterministic serialization for state files, ported
// from core/src/state/canonical.ts: object keys sorted recursively (UTF-16
// code-unit order via Go string comparison, which matches for BMP keys),
// arrays kept in order, JSON.stringify-compatible escaping.
//
// CAS hashes are stable across processes and runs because both the hash input
// and the file bytes are this exact serialization.
func CanonicalJSON(value any) (string, error) {
	var sb strings.Builder
	if err := writeCanonical(&sb, value); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func writeCanonical(sb *strings.Builder, value any) error {
	switch v := value.(type) {
	case nil:
		sb.WriteString("null")
		return nil
	case string:
		writeJSONString(sb, v)
		return nil
	case bool:
		if v {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
		return nil
	case json.Number:
		sb.WriteString(v.String())
		return nil
	case float64:
		return writeNumber(sb, v)
	case int:
		fmt.Fprintf(sb, "%d", v)
		return nil
	case int64:
		fmt.Fprintf(sb, "%d", v)
		return nil
	case []any:
		sb.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				sb.WriteByte(',')
			}
			if err := writeCanonical(sb, item); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
		return nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				sb.WriteByte(',')
			}
			writeJSONString(sb, k)
			sb.WriteByte(':')
			if err := writeCanonical(sb, v[k]); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
		return nil
	case json.RawMessage:
		// Re-parse into generic form to canonicalize.
		var generic any
		dec := json.NewDecoder(bytes.NewReader(v))
		dec.UseNumber()
		if err := dec.Decode(&generic); err != nil {
			return err
		}
		return writeCanonical(sb, generic)
	default:
		return &Error{Code: CodeState, Msg: fmt.Sprintf("canonicalJson: unsupported value type %T in state files", value)}
	}
}

func writeNumber(sb *strings.Builder, f float64) error {
	// JSON.stringify semantics: integers print without a decimal point.
	if f == float64(int64(f)) && f < 1e15 && f > -1e15 {
		fmt.Fprintf(sb, "%d", int64(f))
		return nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	sb.Write(b)
	return nil
}

func writeJSONString(sb *strings.Builder, s string) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // adds trailing newline; strip it
	out := strings.TrimSuffix(buf.String(), "\n")
	sb.WriteString(out)
}
