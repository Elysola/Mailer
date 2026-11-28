package component

import (
	"fmt"
	"os"
	"strings"
)

// readSecretFile reads a secret from a file, trimming trailing whitespace —
// secret files conventionally end in a newline that is never part of the
// credential itself.
func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read secret file %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}

// ExpandEnv expands ${VAR} references in a config value. Applied on top of
// the final value so either style works:
//
//	password_file: /run/secrets/imap_pass   # pre-decrypted secret file
//	password: ${MAILER_PASS}                 # environment variable
func ExpandEnv(v string) string {
	return os.ExpandEnv(v)
}

// ExpandMap expands environment variables in every string leaf of a raw
// config block. Component factories call this before Decode so ${VAR}
// references work uniformly without each component re-implementing expansion.
func ExpandMap(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		out[k] = expandValue(v)
	}
	return out
}

func expandValue(v any) any {
	switch t := v.(type) {
	case string:
		return ExpandEnv(t)
	case map[string]any:
		return ExpandMap(t)
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = expandValue(item)
		}
		return out
	default:
		return v
	}
}
