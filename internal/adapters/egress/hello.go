package egress

import (
	"encoding/binary"
	"errors"
	"io"
)

const (
	recordHandshake     = 0x16
	handshakeClientHelo = 0x01
	maxRecordBytes      = 16384 + 256
	maxHelloBytes       = 64 << 10
	extServerName       = 0x0000
	extEncryptedHello   = 0xfe0d
	nameTypeHost        = 0x00
)

var (
	errNotTLS    = errors.New("not a TLS ClientHello")
	errNoSNI     = errors.New("ClientHello names no server")
	errECH       = errors.New("ClientHello carries encrypted_client_hello")
	errHelloSize = errors.New("ClientHello too large")
)

func readClientHello(src io.Reader) ([]byte, string, error) {
	var raw, body []byte
	want := -1
	for want < 0 || len(body) < want {
		var head [5]byte
		if _, err := io.ReadFull(src, head[:]); err != nil {
			return nil, "", errNotTLS
		}
		size := int(binary.BigEndian.Uint16(head[3:]))
		if head[0] != recordHandshake || head[1] != 0x03 || size == 0 || size > maxRecordBytes {
			return nil, "", errNotTLS
		}
		if len(raw)+len(head)+size > maxHelloBytes {
			return nil, "", errHelloSize
		}
		frag := make([]byte, size)
		if _, err := io.ReadFull(src, frag); err != nil {
			return nil, "", errNotTLS
		}
		raw = append(raw, head[:]...)
		raw = append(raw, frag...)
		body = append(body, frag...)
		if want < 0 && len(body) >= 4 {
			if body[0] != handshakeClientHelo {
				return nil, "", errNotTLS
			}
			want = 4 + (int(body[1])<<16 | int(body[2])<<8 | int(body[3]))
			if want > maxHelloBytes {
				return nil, "", errHelloSize
			}
		}
	}
	name, err := serverName(body[4:want])
	if err != nil {
		return nil, "", err
	}
	return raw, name, nil
}

type cursor struct {
	b   []byte
	bad bool
}

func (c *cursor) take(n int) []byte {
	if c.bad || n < 0 || n > len(c.b) {
		c.bad = true
		return nil
	}
	out := c.b[:n]
	c.b = c.b[n:]
	return out
}

func (c *cursor) u8() int {
	b := c.take(1)
	if b == nil {
		return 0
	}
	return int(b[0])
}

func (c *cursor) u16() int {
	b := c.take(2)
	if b == nil {
		return 0
	}
	return int(binary.BigEndian.Uint16(b))
}

func serverName(hello []byte) (string, error) {
	c := &cursor{b: hello}
	c.take(2 + 32)
	c.take(c.u8())
	c.take(c.u16())
	c.take(c.u8())
	exts := &cursor{b: c.take(c.u16())}
	if c.bad || len(c.b) != 0 {
		return "", errNotTLS
	}
	name, seen := "", false
	for len(exts.b) > 0 {
		kind := exts.u16()
		data := exts.take(exts.u16())
		if exts.bad {
			return "", errNotTLS
		}
		switch kind {
		case extEncryptedHello:
			return "", errECH
		case extServerName:
			if seen {
				return "", errNotTLS
			}
			seen = true
			got, err := hostName(data)
			if err != nil {
				return "", err
			}
			name = got
		}
	}
	if name == "" {
		return "", errNoSNI
	}
	return name, nil
}

func hostName(data []byte) (string, error) {
	c := &cursor{b: data}
	list := &cursor{b: c.take(c.u16())}
	if c.bad || len(c.b) != 0 {
		return "", errNotTLS
	}
	name := ""
	for len(list.b) > 0 {
		kind := list.u8()
		value := list.take(list.u16())
		if list.bad {
			return "", errNotTLS
		}
		if kind != nameTypeHost {
			continue
		}
		if name != "" || len(value) == 0 {
			return "", errNotTLS
		}
		name = string(value)
	}
	return name, nil
}
