package cmd

import "testing"

func TestAccumulatorMultiLine(t *testing.T) {
	var a accumulator
	a.feed("SELECT 1")
	if _, complete := a.take(); complete {
		t.Fatal("statement without ; should not be complete")
	}
	if !a.pending() {
		t.Fatal("should be pending after a partial line")
	}
	a.feed("FROM answers;")
	stmt, complete := a.take()
	if !complete {
		t.Fatal("statement with ; should complete")
	}
	if stmt != "SELECT 1\nFROM answers" {
		t.Fatalf("accumulated statement = %q", stmt)
	}
	if a.pending() {
		t.Fatal("should not be pending after completion")
	}
}

func TestAccumulatorSemicolonInString(t *testing.T) {
	var a accumulator
	a.feed("SELECT ';' AS x;")
	stmt, complete := a.take()
	if !complete {
		t.Fatal("should complete on the terminating ;")
	}
	if stmt != "SELECT ';' AS x" {
		t.Fatalf("statement = %q (a ; inside a string literal must not terminate)", stmt)
	}
}

func TestAccumulatorEscapedQuote(t *testing.T) {
	var a accumulator
	a.feed("SELECT 'a''b;c' AS x;")
	stmt, complete := a.take()
	if !complete || stmt != "SELECT 'a''b;c' AS x" {
		t.Fatalf("escaped-quote handling wrong: %q complete=%v", stmt, complete)
	}
}

// TestAccumulatorMultiStatement covers the #52 fix: a single line holding two
// statements must yield both, not silently drop everything after the first ';'.
func TestAccumulatorMultiStatement(t *testing.T) {
	var a accumulator
	a.feed("SELECT 1; SELECT 2;")

	stmt, complete := a.take()
	if !complete || stmt != "SELECT 1" {
		t.Fatalf("first take = %q complete=%v, want %q", stmt, complete, "SELECT 1")
	}
	stmt, complete = a.take()
	if !complete || stmt != " SELECT 2" {
		t.Fatalf("second take = %q complete=%v, want %q", stmt, complete, " SELECT 2")
	}
	if _, complete := a.take(); complete {
		t.Fatal("no third statement should remain")
	}
	if a.pending() {
		t.Fatal("buffer should be empty after draining all statements")
	}
}
