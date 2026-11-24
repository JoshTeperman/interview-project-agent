// Package prompts holds the model's standing instructions as versioned files.
// They live outside Go source so prompt changes show up as plain-text diffs.
package prompts

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
)

//go:embed system.md
var system string

// System returns the standing instructions for the chat model.
func System() string { return system }

// Version identifies the current prompt text: the first 12 hex digits of its
// SHA-256. It is logged with each question, so answers can be traced to a prompt.
func Version() string {
	h := sha256.Sum256([]byte(system))
	return hex.EncodeToString(h[:])[:12]
}
