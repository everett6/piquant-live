package collect

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"
)

// maxWSMessage bounds one (possibly fragmented) WebSocket message. A level2 snapshot of BTC-USD is a few MB.
const maxWSMessage = 32 << 20

// wsConn is a minimal RFC 6455 client: masked text writes, FIN-aware reads (fragments are joined).
type wsConn struct {
	conn net.Conn
	br   *bufio.Reader
}

func dialWS(ctx context.Context, endpoint string) (*wsConn, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	d := net.Dialer{Timeout: 8 * time.Second, KeepAlive: 15 * time.Second}
	raw, err := d.DialContext(dctx, "tcp", host)
	if err != nil {
		return nil, err
	}
	var conn net.Conn = raw
	if u.Scheme == "wss" {
		tc := tls.Client(raw, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
		if err := tc.HandshakeContext(dctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		conn = tc
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	path := u.RequestURI()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", path, u.Host, base64.StdEncoding.EncodeToString(key))
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := conn.Write([]byte(req)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	br := bufio.NewReaderSize(conn, 256<<10)
	status, err := br.ReadString('\n')
	if err != nil || !strings.Contains(status, " 101") {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket handshake: %q %v", strings.TrimSpace(status), err)
	}
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return &wsConn{conn: conn, br: br}, nil
}

func (w *wsConn) Close() error { return w.conn.Close() }

// writeFrame sends one masked client frame.
func (w *wsConn) writeFrame(op byte, payload []byte) error {
	n := len(payload)
	hdr := []byte{0x80 | op}
	switch {
	case n <= 125:
		hdr = append(hdr, 0x80|byte(n))
	case n <= 0xFFFF:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		hdr = binary.BigEndian.AppendUint64(hdr, uint64(n))
	}
	var mask [4]byte
	_, _ = rand.Read(mask[:])
	hdr = append(hdr, mask[:]...)
	buf := make([]byte, 0, len(hdr)+n)
	buf = append(buf, hdr...)
	for i, b := range payload {
		buf = append(buf, b^mask[i%4])
	}
	_ = w.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := w.conn.Write(buf)
	return err
}

func (w *wsConn) WriteText(s string) error { return w.writeFrame(0x1, []byte(s)) }

// ReadMessage returns the next data message. Pings are answered, pongs skipped, close returns io.EOF.
func (w *wsConn) ReadMessage(timeout time.Duration) ([]byte, error) {
	var msg []byte
	for {
		_ = w.conn.SetReadDeadline(time.Now().Add(timeout))
		fin, op, payload, err := readFrame(w.br)
		if err != nil {
			return nil, err
		}
		switch op {
		case 0x8:
			return nil, io.EOF
		case 0x9:
			_ = w.writeFrame(0xA, payload)
			continue
		case 0xA:
			continue
		}
		if len(msg)+len(payload) > maxWSMessage {
			return nil, errors.New("websocket message too large")
		}
		msg = append(msg, payload...)
		if fin {
			return msg, nil
		}
	}
}

func readFrame(r io.Reader) (fin bool, op byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	fin, op = h[0]&0x80 != 0, h[0]&0x0F
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7F)
	switch n {
	case 126:
		var e [2]byte
		if _, err = io.ReadFull(r, e[:]); err != nil {
			return
		}
		n = uint64(binary.BigEndian.Uint16(e[:]))
	case 127:
		var e [8]byte
		if _, err = io.ReadFull(r, e[:]); err != nil {
			return
		}
		n = binary.BigEndian.Uint64(e[:])
	}
	if n > maxWSMessage {
		return false, 0, nil, errors.New("websocket frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err = io.ReadFull(r, mask[:]); err != nil {
			return
		}
	}
	payload = make([]byte, n)
	if _, err = io.ReadFull(r, payload); err != nil {
		return
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return
}
