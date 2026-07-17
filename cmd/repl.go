package cmd

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/fsutil"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/ergochat/readline"
	"github.com/spf13/cobra"
)

func newReplCmd() *cobra.Command {
	var datasetRefs, allowDirs []string
	cmd := &cobra.Command{
		Use:   "repl --dataset <ref>",
		Short: "Interactive SQL session over one or more datasets",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			specs, err := resolveDatasetSpecs(datasetRefs)
			if err != nil {
				return err
			}
			e, err := duck.Open(context.Background(), specs, allowDirs, output.Stderr())
			if err != nil {
				return output.Internalf("%v", err)
			}
			defer e.Close()
			return runRepl(e)
		},
	}
	cmd.Flags().StringArrayVar(&datasetRefs, "dataset", nil, "dataset ref <portal>/<name> (repeatable)")
	cmd.Flags().StringArrayVar(&allowDirs, "allow-dir", nil, "additional directory the session may read (repeatable)")
	return cmd
}

func runRepl(e *duck.Engine) error {
	historyFile, err := replHistoryPath()
	if err == nil {
		fsutil.PreCreate0600(historyFile)
	}
	rl, err := readline.NewFromConfig(&readline.Config{
		Prompt:      "cc-data> ",
		HistoryFile: historyFile,
	})
	if err != nil {
		return output.Internalf("starting repl: %v", err)
	}
	defer rl.Close()

	var acc accumulator
	for {
		if acc.pending() {
			rl.SetPrompt("     ...> ")
		} else {
			rl.SetPrompt("cc-data> ")
		}
		line, err := rl.ReadLine()
		if err == readline.ErrInterrupt {
			acc.reset()
			continue
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil
		}
		stmt, complete := acc.feed(line)
		if !complete {
			continue
		}
		executeReplStatement(e, stmt, output.Stdout(), output.Stderr())
	}
}

// executeReplStatement runs a completed statement or a dot-command.
func executeReplStatement(e *duck.Engine, stmt string, out, errw io.Writer) {
	trimmed := strings.TrimSpace(stmt)
	if trimmed == "" {
		return
	}
	if strings.HasPrefix(trimmed, ".") {
		if err := handleDotCommand(e, trimmed, out); err != nil {
			fmt.Fprintf(errw, "error: %v\n", err)
		}
		return
	}
	rows, err := e.Query(context.Background(), trimmed)
	if err != nil {
		fmt.Fprintf(errw, "error: %v\n", err)
		return
	}
	defer rows.Close()
	if err := duck.RenderRows(out, rows, duck.FormatTable); err != nil {
		fmt.Fprintf(errw, "error: %v\n", err)
	}
}

// handleDotCommand runs .tables and .schema conveniences.
func handleDotCommand(e *duck.Engine, line string, out io.Writer) error {
	fields := strings.Fields(line)
	switch fields[0] {
	case ".tables":
		return runAndRender(e, out, "SELECT table_name FROM information_schema.tables ORDER BY table_name")
	case ".schema":
		if len(fields) > 1 {
			return runAndRender(e, out, "DESCRIBE "+fields[1])
		}
		return runAndRender(e, out, "SELECT table_name FROM information_schema.tables ORDER BY table_name")
	case ".help":
		fmt.Fprintln(out, "Commands: .tables, .schema [view], .help, .quit")
		return nil
	case ".quit", ".exit":
		fmt.Fprintln(out, "use Ctrl-D to exit")
		return nil
	default:
		return fmt.Errorf("unknown command %q", fields[0])
	}
}

func runAndRender(e *duck.Engine, out io.Writer, query string) error {
	rows, err := e.Query(context.Background(), query)
	if err != nil {
		return err
	}
	defer rows.Close()
	return duck.RenderRows(out, rows, duck.FormatTable)
}

// accumulator collects lines into a statement terminated by a bare ';', ignoring
// ';' inside single-quoted string literals.
type accumulator struct {
	buf strings.Builder
}

func (a *accumulator) pending() bool { return a.buf.Len() > 0 }
func (a *accumulator) reset()        { a.buf.Reset() }

func (a *accumulator) feed(line string) (stmt string, complete bool) {
	// Dot-commands are single-line and complete immediately when nothing is buffered.
	if !a.pending() && strings.HasPrefix(strings.TrimSpace(line), ".") {
		return strings.TrimSpace(line), true
	}
	if a.pending() {
		a.buf.WriteByte('\n')
	}
	a.buf.WriteString(line)
	if idx := terminatingSemicolon(a.buf.String()); idx >= 0 {
		full := a.buf.String()
		a.buf.Reset()
		return full[:idx], true
	}
	return "", false
}

// terminatingSemicolon returns the index of the first ';' outside a single-quoted
// string, or -1.
func terminatingSemicolon(s string) int {
	inStr := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' {
			// A doubled '' inside a string is an escaped quote.
			if inStr && i+1 < len(s) && s[i+1] == '\'' {
				i++
				continue
			}
			inStr = !inStr
		} else if c == ';' && !inStr {
			return i
		}
	}
	return -1
}

func replHistoryPath() (string, error) {
	dir, err := config.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "repl_history"), nil
}
