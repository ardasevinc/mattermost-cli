package conformance

import "bytes"

type limitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.buffer.Len()
	if remaining > 0 {
		write := len(p)
		if write > remaining {
			write = remaining
		}
		_, _ = b.buffer.Write(p[:write])
	}
	if len(p) > remaining {
		b.exceeded = true
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buffer.String()
}

func (b *limitedBuffer) Exceeded() bool {
	return b.exceeded
}
