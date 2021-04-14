package tail

import (
	"context"
	"io"
)

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (r ctxReader) Read(data []byte) (int, error) {
	type result struct {
		n   int
		err error
	}

	ch := make(chan result, 1)
	go func() {
		n, err := r.r.Read(data)
		ch <- result{
			n:   n,
			err: err,
		}
	}()
	select {
	case ret := <-ch:
		return ret.n, ret.err
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	}
}
