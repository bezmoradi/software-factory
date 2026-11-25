package remote

import (
	"bufio"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/bezmoradi/software-factory/internal/core/session"
	"github.com/go-git/go-git/v5/plumbing"
)

const (
	packHeader    = 12
	maxSizeShift  = 50
	maxOffsetLen  = 10
	countBuffer   = 64 << 10
	refDeltaBytes = 20
)

var errStopped = errors.New("the count stopped")

type meter struct {
	report   func(session.CloneProgress)
	mu       sync.Mutex
	written  atomic.Int64
	objects  atomic.Int64
	total    atomic.Int64
	checked  atomic.Int64
	arrived  atomic.Bool
	finished atomic.Bool
	wake     chan struct{}
	stop     chan struct{}
	done     chan struct{}
	ended    sync.Once
}

func newMeter(report func(session.CloneProgress), f *os.File) *meter {
	m := &meter{report: report, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}
	go m.count(f)
	return m
}

func (m *meter) send() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.report(session.CloneProgress{
		Received: m.written.Load(),
		Objects:  m.objects.Load(),
		Total:    m.total.Load(),
		Arrived:  m.arrived.Load(),
		Checked:  m.checked.Load(),
	})
}

func (m *meter) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *meter) wrote(n int) {
	m.written.Add(int64(n))
	m.signal()
	m.send()
}

func (m *meter) received() {
	m.finished.Store(true)
	m.signal()
	m.arrived.Store(true)
	m.send()
}

func (m *meter) end() {
	m.ended.Do(func() {
		m.finished.Store(true)
		close(m.stop)
		<-m.done
	})
}

func (m *meter) count(f *os.File) {
	defer close(m.done)
	defer func() { _ = recover() }()
	r := bufio.NewReaderSize(&tail{f: f, m: m}, countBuffer)
	var head [packHeader]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return
	}
	if string(head[:4]) != "PACK" {
		return
	}
	if v := binary.BigEndian.Uint32(head[4:8]); v != 2 && v != 3 {
		return
	}
	total := int64(binary.BigEndian.Uint32(head[8:12]))
	m.total.Store(total)
	m.send()
	var z io.ReadCloser
	for i := int64(1); i <= total; i++ {
		size, ok := objectHeader(r)
		if !ok {
			return
		}
		var err error
		if z == nil {
			z, err = zlib.NewReader(r)
		} else if rs, can := z.(zlib.Resetter); can {
			err = rs.Reset(r, nil)
		} else {
			return
		}
		if err != nil {
			return
		}
		n, err := io.CopyN(io.Discard, z, size+1)
		if !errors.Is(err, io.EOF) || n != size {
			return
		}
		m.objects.Store(i)
		m.send()
	}
}

func objectHeader(r *bufio.Reader) (int64, bool) {
	c, err := r.ReadByte()
	if err != nil {
		return 0, false
	}
	kind := plumbing.ObjectType((c >> 4) & 7)
	size := int64(c & 15)
	for shift := 4; c&0x80 != 0; shift += 7 {
		if shift > maxSizeShift {
			return 0, false
		}
		if c, err = r.ReadByte(); err != nil {
			return 0, false
		}
		size += int64(c&0x7f) << shift
	}
	switch kind {
	case plumbing.CommitObject, plumbing.TreeObject, plumbing.BlobObject, plumbing.TagObject:
	case plumbing.OFSDeltaObject:
		for i := 0; ; i++ {
			if i >= maxOffsetLen {
				return 0, false
			}
			b, err := r.ReadByte()
			if err != nil {
				return 0, false
			}
			if b&0x80 == 0 {
				break
			}
		}
	case plumbing.REFDeltaObject:
		if _, err := r.Discard(refDeltaBytes); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return size, true
}

type tail struct {
	f   *os.File
	m   *meter
	off int64
}

func (t *tail) Read(p []byte) (int, error) {
	for {
		select {
		case <-t.m.stop:
			return 0, errStopped
		default:
		}
		end := t.m.written.Load()
		if t.off < end {
			n, err := t.f.ReadAt(p[:min(int64(len(p)), end-t.off)], t.off)
			t.off += int64(n)
			if n > 0 {
				return n, nil
			}
			if err == nil {
				err = io.ErrNoProgress
			}
			return 0, err
		}
		if t.m.finished.Load() {
			return 0, io.EOF
		}
		select {
		case <-t.m.wake:
		case <-t.m.stop:
			return 0, errStopped
		}
	}
}

type meteredFile struct {
	f *os.File
	m *meter
}

func (w meteredFile) Write(p []byte) (int, error) {
	n, err := w.f.Write(p)
	if n > 0 {
		w.m.wrote(n)
	}
	return n, err
}

type checkCount struct{ m *meter }

func (o checkCount) OnHeader(uint32) error { return nil }

func (o checkCount) OnInflatedObjectHeader(plumbing.ObjectType, int64, int64) error { return nil }

func (o checkCount) OnInflatedObjectContent(plumbing.Hash, int64, uint32, []byte) error {
	o.m.checked.Add(1)
	o.m.send()
	return nil
}

func (o checkCount) OnFooter(plumbing.Hash) error { return nil }
