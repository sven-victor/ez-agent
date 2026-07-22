package message

import (
	"crypto/rand"
	"fmt"
)

// NewID returns a new random message ID (UUID v4 string).
// Suitable as a MySQL/primary-key style identifier for one row per message.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Extremely unlikely; still return a unique-enough value.
		return fmt.Sprintf("msg-%p", &b)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// EnsureID assigns NewID when m.ID is empty. Returns true if an ID was assigned.
func EnsureID(m *Message) bool {
	if m == nil || m.ID != "" {
		return false
	}
	m.ID = NewID()
	return true
}

// EnsureIDs assigns IDs to any messages that lack one. Returns true if any changed.
func EnsureIDs(msgs []Message) bool {
	changed := false
	for i := range msgs {
		if EnsureID(&msgs[i]) {
			changed = true
		}
	}
	return changed
}
