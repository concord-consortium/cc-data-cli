package cmd

import "testing"

func TestAccumulatorMultiLine(t *testing.T) {
	var a accumulator
	if _, complete := a.feed("SELECT 1"); complete {
		t.Fatal("statement without ; should not be complete")
	}
	if !a.pending() {
		t.Fatal("should be pending after a partial line")
	}
	stmt, complete := a.feed("FROM answers;")
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
	stmt, complete := a.feed("SELECT ';' AS x;")
	if !complete {
		t.Fatal("should complete on the terminating ;")
	}
	if stmt != "SELECT ';' AS x" {
		t.Fatalf("statement = %q (a ; inside a string literal must not terminate)", stmt)
	}
}

func TestAccumulatorEscapedQuote(t *testing.T) {
	var a accumulator
	stmt, complete := a.feed("SELECT 'a''b;c' AS x;")
	if !complete || stmt != "SELECT 'a''b;c' AS x" {
		t.Fatalf("escaped-quote handling wrong: %q complete=%v", stmt, complete)
	}
}

func TestAccumulatorDotCommand(t *testing.T) {
	var a accumulator
	stmt, complete := a.feed(".tables")
	if !complete || stmt != ".tables" {
		t.Fatalf("dot command should complete immediately: %q %v", stmt, complete)
	}
}
