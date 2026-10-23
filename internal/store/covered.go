package store

import "errors"

// KeySource yields identity keys in strictly increasing order, returning an error otherwise.
type KeySource interface {
	Next() (key string, ok bool, err error)
}

type coverHead struct {
	src     KeySource
	key     string
	ok      bool
	started bool
}

func (h *coverHead) advance() error {
	key, ok, err := h.src.Next()
	if err != nil {
		return err
	}
	h.key, h.ok, h.started = key, ok, true
	return nil
}

// Covered answers, for keys asked in non-decreasing order, whether any source holds the
// key; a source's order error comes back from Has or Drain.
type Covered struct {
	heads []*coverHead
	lastQ string
	asked bool
}

// NewCovered reads the sources lazily, from Has and Drain.
func NewCovered(srcs ...KeySource) *Covered {
	c := &Covered{}
	for _, s := range srcs {
		c.heads = append(c.heads, &coverHead{src: s})
	}
	return c
}

// Has reports whether any source holds key.
func (c *Covered) Has(key string) (bool, error) {
	if c.asked && key < c.lastQ {
		return false, errors.New("covered: keys asked out of order")
	}
	c.lastQ, c.asked = key, true
	hit := false
	for _, h := range c.heads {
		if !h.started {
			if err := h.advance(); err != nil {
				return false, err
			}
		}
		for h.ok && h.key < key {
			if err := h.advance(); err != nil {
				return false, err
			}
		}
		if h.ok && h.key == key {
			hit = true
		}
	}
	return hit, nil
}

// Drain reads every source to its end, checking order. StreamMerge calls it after the old
// store is exhausted: a source whose disorder sits past the last old key would otherwise
// never be read, and its out-of-order key could have been a record the walk dropped.
func (c *Covered) Drain() error {
	for _, h := range c.heads {
		if !h.started {
			if err := h.advance(); err != nil {
				return err
			}
		}
		for h.ok {
			if err := h.advance(); err != nil {
				return err
			}
		}
	}
	return nil
}
