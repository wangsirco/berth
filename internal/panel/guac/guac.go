// Package guac speaks the Guacamole protocol to guacd and relays it over a WebSocket to
// guacamole-common-js in the browser (方案 §5.8), so no Java web application is needed.
//
// Wire format: an instruction is LEN.VALUE elements joined by "," and ended by ";", where
// LEN counts Unicode characters (not bytes). The first element is the opcode.
package guac

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// Encode builds one instruction.
func Encode(op string, args ...string) string {
	var b strings.Builder
	el := append([]string{op}, args...)
	for i, e := range el {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.Itoa(utf8.RuneCountInString(e)))
		b.WriteByte('.')
		b.WriteString(e)
	}
	b.WriteByte(';')
	return b.String()
}

// Reader reads whole instructions from guacd.
type Reader struct{ r *bufio.Reader }

func NewReader(r io.Reader) *Reader { return &Reader{bufio.NewReaderSize(r, 64<<10)} }

// Read returns the next instruction as raw text (for relaying) and its elements.
func (rd *Reader) Read() (string, []string, error) {
	var raw strings.Builder
	var els []string
	for {
		// length
		lenStr, err := rd.r.ReadString('.')
		if err != nil {
			return "", nil, err
		}
		n, err := strconv.Atoi(lenStr[:len(lenStr)-1])
		if err != nil || n < 0 || n > 16<<20 {
			return "", nil, fmt.Errorf("guac: bad element length %q", lenStr)
		}
		raw.WriteString(lenStr)
		// n characters (runes)
		var el strings.Builder
		for i := 0; i < n; i++ {
			c, _, err := rd.r.ReadRune()
			if err != nil {
				return "", nil, err
			}
			el.WriteRune(c)
		}
		s := el.String()
		raw.WriteString(s)
		els = append(els, s)
		term, err := rd.r.ReadByte()
		if err != nil {
			return "", nil, err
		}
		raw.WriteByte(term)
		switch term {
		case ',':
			continue
		case ';':
			return raw.String(), els, nil
		default:
			return "", nil, fmt.Errorf("guac: bad terminator %q", term)
		}
	}
}

// Parse splits one or more instructions sent by the browser.
func Parse(s string) ([][]string, error) {
	var out [][]string
	rd := NewReader(strings.NewReader(s))
	for {
		_, els, err := rd.Read()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, els)
	}
}

// Params of one connection: protocol and its arguments (guacd names: hostname, port,
// username, password, domain, security, ignore-cert, …).
type Params struct {
	Protocol string
	Args     map[string]string
	Width    int
	Height   int
	DPI      int
	Timezone string // IANA name from the browser, e.g. Pacific/Auckland
}

// Handshake connects to guacd and negotiates the connection; it returns the open socket
// and a reader positioned after "ready".
func Handshake(ctx context.Context, addr string, p Params) (net.Conn, *Reader, string, error) {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, "", fmt.Errorf("guacd not reachable: %w", err)
	}
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	rd := NewReader(conn)
	send := func(op string, args ...string) error {
		_, err := io.WriteString(conn, Encode(op, args...))
		return err
	}
	if err := send("select", p.Protocol); err != nil {
		conn.Close()
		return nil, nil, "", err
	}
	_, args, err := rd.Read()
	if err != nil || len(args) == 0 || args[0] != "args" {
		conn.Close()
		return nil, nil, "", fmt.Errorf("guacd did not offer arguments (%v)", err)
	}
	if p.Width <= 0 {
		p.Width, p.Height = 1280, 800
	}
	if p.DPI <= 0 {
		p.DPI = 96
	}
	send("size", strconv.Itoa(p.Width), strconv.Itoa(p.Height), strconv.Itoa(p.DPI))
	send("audio", "audio/L16")
	send("video")
	send("image", "image/png", "image/jpeg", "image/webp")
	if p.Timezone == "" {
		p.Timezone = "UTC"
	}
	send("timezone", p.Timezone)
	values := []string{}
	for _, name := range args[1:] {
		if strings.HasPrefix(name, "VERSION_") { // the protocol version comes first; echo it
			values = append(values, name)
			continue
		}
		values = append(values, p.Args[name])
	}
	if err := send("connect", values...); err != nil {
		conn.Close()
		return nil, nil, "", err
	}
	_, ready, err := rd.Read()
	if err != nil {
		conn.Close()
		return nil, nil, "", err
	}
	if ready[0] == "error" {
		conn.Close()
		msg := "connection refused"
		if len(ready) > 1 {
			msg = ready[1]
		}
		return nil, nil, "", errors.New(msg)
	}
	if ready[0] != "ready" || len(ready) < 2 {
		conn.Close()
		return nil, nil, "", fmt.Errorf("guacd answered %q", ready[0])
	}
	conn.SetDeadline(time.Time{})
	return conn, rd, ready[1], nil
}

// Relay pumps instructions between guacd and the browser until either side ends.
func Relay(ctx context.Context, ws *websocket.Conn, conn net.Conn, rd *Reader, id string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer conn.Close()
	ws.SetReadLimit(4 << 20)
	// the browser learns the tunnel's id from an internal instruction
	if err := ws.Write(ctx, websocket.MessageText, []byte(Encode("", id))); err != nil {
		return err
	}
	errc := make(chan error, 2)
	go func() { // guacd → browser, whole instructions (batched while more are ready)
		var batch strings.Builder
		for {
			raw, _, err := rd.Read()
			if err != nil {
				errc <- err
				return
			}
			batch.WriteString(raw)
			if rd.r.Buffered() > 0 && batch.Len() < 1<<20 {
				continue
			}
			if err := ws.Write(ctx, websocket.MessageText, []byte(batch.String())); err != nil {
				errc <- err
				return
			}
			batch.Reset()
		}
	}()
	go func() { // browser → guacd; internal instructions (ping) are answered here
		for {
			_, msg, err := ws.Read(ctx)
			if err != nil {
				errc <- err
				return
			}
			s := string(msg)
			if strings.HasPrefix(s, "0.,") {
				if ins, err := Parse(s); err == nil && len(ins) > 0 && len(ins[0]) > 1 && ins[0][1] == "ping" {
					ws.Write(ctx, websocket.MessageText, msg) // echo the ping
				}
				continue
			}
			if _, err := io.WriteString(conn, s); err != nil {
				errc <- err
				return
			}
		}
	}()
	err := <-errc
	ws.Close(websocket.StatusNormalClosure, "")
	return err
}
