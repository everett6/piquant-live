package paper

import (
	"crypto/rand"
	"errors"
	"io"
)

// sendWSText writes a masked RFC 6455 text frame to the connection.
// Uses a random mask key per frame as required by RFC 6455 for client-to-server frames.
func sendWSText(w io.Writer, msg string) error {
	b := []byte(msg)
	n := len(b)
	var header []byte
	if n <= 125 {
		header = []byte{0x81, 0x80 | byte(n)}
	} else if n <= 65535 {
		header = []byte{0x81, 0x80 | 126, byte(n >> 8), byte(n & 0xFF)}
	} else {
		return errors.New("frame too large")
	}

	// Generate random mask key per RFC 6455
	mask := make([]byte, 4)
	if _, err := rand.Read(mask); err != nil {
		return err
	}
	masked := make([]byte, n)
	for i := 0; i < n; i++ {
		masked[i] = b[i] ^ mask[i%4]
	}

	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(mask); err != nil {
		return err
	}
	_, err := w.Write(masked)
	return err
}
