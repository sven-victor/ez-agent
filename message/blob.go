package message

import "fmt"

// Blob carries binary media. Exactly one of Bytes, URL, or Path should be set.
type Blob struct {
	MIME     string
	Bytes    []byte
	URL      string
	Path     string
	Filename string
	Size     int64
}

// Validate reports whether the blob has a usable reference.
func (b Blob) Validate() error {
	n := 0
	if len(b.Bytes) > 0 {
		n++
	}
	if b.URL != "" {
		n++
	}
	if b.Path != "" {
		n++
	}
	if n == 0 {
		return fmt.Errorf("blob: exactly one of Bytes, URL, or Path must be set")
	}
	if n > 1 {
		return fmt.Errorf("blob: only one of Bytes, URL, or Path may be set")
	}
	if b.MIME == "" {
		return fmt.Errorf("blob: MIME is required")
	}
	return nil
}
