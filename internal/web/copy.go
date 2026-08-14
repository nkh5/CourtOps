package web

import (
	"fmt"
	"io"
)

// copyLimited copies at most limit bytes and reports how many it wrote, so the
// caller can tell "exactly at the limit" from "one byte too many".
func copyLimited(dst io.Writer, src io.Reader, limit int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, limit))
	if err != nil {
		return n, fmt.Errorf("read upload: %w", err)
	}
	return n, nil
}
