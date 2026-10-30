package store

import (
	"errors"
	"testing"
)

type sliceSource struct {
	keys []string
	i    int
}

func (s *sliceSource) Next() (string, bool, error) {
	if s.i >= len(s.keys) {
		return "", false, nil
	}
	k := s.keys[s.i]
	s.i++
	return k, true, nil
}

// orderedSource refuses a key not above the one before it, as MembershipReader does.
type orderedSource struct {
	sliceSource
	last string
}

func (s *orderedSource) Next() (string, bool, error) {
	k, ok, err := s.sliceSource.Next()
	if ok && s.last != "" && k <= s.last {
		return "", false, errOutOfOrder
	}
	s.last = k
	return k, ok, err
}

var errOutOfOrder = errors.New("out of order")

func TestCoveredAnswersFromEverySource(t *testing.T) {
	c := NewCovered(&sliceSource{keys: []string{"b", "d"}}, &sliceSource{keys: []string{"a", "d", "e"}})
	for _, q := range []struct {
		key  string
		want bool
	}{{"a", true}, {"b", true}, {"c", false}, {"d", true}, {"e", true}, {"f", false}} {
		got, err := c.Has(q.key)
		if err != nil || got != q.want {
			t.Fatalf("Has(%q) = %v, %v; want %v", q.key, got, err, q.want)
		}
	}
	if err := c.Drain(); err != nil {
		t.Fatal(err)
	}
}

func TestCoveredRefusesKeysAskedOutOfOrder(t *testing.T) {
	c := NewCovered(&sliceSource{keys: []string{"a"}})
	if _, err := c.Has("b"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Has("a"); err == nil {
		t.Fatal("a key asked below the previous one must be an error")
	}
}

func TestCoveredFailsInHasOnASourceOutOfOrder(t *testing.T) {
	c := NewCovered(&orderedSource{sliceSource: sliceSource{keys: []string{"a", "c", "b", "d"}}})
	for _, key := range []string{"a", "b"} {
		if _, err := c.Has(key); err != nil {
			t.Fatalf("Has(%q): %v", key, err)
		}
	}
	if _, err := c.Has("d"); !errors.Is(err, errOutOfOrder) {
		t.Fatalf("Has(d) err = %v, want the source's order error before Drain", err)
	}
}
