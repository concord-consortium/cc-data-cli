// Package mcpserver exposes the data-and-analysis command surface as a stdio MCP
// server whose handlers call the same internal cores as the CLI.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/guidance"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Options configure the MCP server.
type Options struct {
	Version   string
	AllowDirs []string // extends the query sandbox, from cc-data mcp --allow-dir launch args
}

func ptr(b bool) *bool { return &b }

// NewServer assembles the server with the pinned tool surface and the guidance it
// advertises in the initialize response.
func NewServer(opts Options) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "cc-data", Version: opts.Version},
		&mcp.ServerOptions{Instructions: guidance.Instructions()},
	)
	registerTools(s, opts)
	return s
}

// addTool registers a tool whose handler errors keep their machine-readable envelope.
// The SDK renders a handler error with Error(), which for a CLIError is the message alone,
// so the code and action the guidance tells the model to act on would never reach it.
func addTool[In, Out any](s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		res, out, err := h(ctx, req, in)
		return res, out, codedError(err)
	})
}

// codedError renders a CLIError as the JSON envelope the CLI prints with --json, so a tool
// caller sees the same code, message and action a terminal user does. Anything else passes through.
func codedError(err error) error {
	var ce *output.CLIError
	if !errors.As(err, &ce) {
		return err
	}
	b, jsonErr := json.Marshal(ce.Envelope())
	if jsonErr != nil {
		return err
	}
	return errors.New(string(b))
}

// Run starts the stdio MCP server.
func Run(ctx context.Context, opts Options) error {
	return NewServer(opts).Run(ctx, &mcp.StdioTransport{})
}

// progressWriter forwards fetch progress lines as MCP progress notifications when
// the client supplied a progress token; otherwise it discards.
type progressWriter struct {
	ctx     context.Context
	session *mcp.ServerSession
	token   any
	n       float64
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if p.token != nil && p.session != nil {
		p.n++
		p.session.NotifyProgress(p.ctx, &mcp.ProgressNotificationParams{
			ProgressToken: p.token,
			Message:       strings.TrimSpace(string(b)),
			Progress:      p.n,
		})
	}
	return len(b), nil
}

func newProgress(ctx context.Context, req *mcp.CallToolRequest) io.Writer {
	tok := req.Params.GetProgressToken()
	if tok == nil {
		return io.Discard
	}
	return &progressWriter{ctx: ctx, session: req.Session, token: tok}
}

// loadRuntime mirrors the CLI's config + data-root resolution.
func loadRuntime() (*config.Config, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, "", err
	}
	root, err := cfg.DataRootDir()
	if err != nil {
		return nil, "", err
	}
	return cfg, root, nil
}

// openDataset resolves a ref that names a dataset already on disk. Every MCP
// tool that opens a dataset (all but dataset_create) goes through it, so it uses
// the salvaging parser: dataset_list builds its rows from folder names, and this
// has to reach anything it shows, including a folder under a portal the strict
// parser would refuse.
func openDataset(refStr string) (*dataset.Dataset, config.Portal, error) {
	cfg, root, err := loadRuntime()
	if err != nil {
		return nil, config.Portal{}, err
	}
	ref, err := dataset.ParseRefForExisting(cfg, root, refStr)
	if err != nil {
		return nil, config.Portal{}, err
	}
	d := dataset.Open(root, ref)
	if !d.Exists() {
		return nil, config.Portal{}, &output.CLIError{Code: "NOT_FOUND", Message: "dataset " + ref.String() + " does not exist"}
	}
	return d, ref.Portal, nil
}

func openForFetch(refStr string) (*dataset.Dataset, *api.Client, error) {
	d, portal, err := openDataset(refStr)
	if err != nil {
		return nil, nil, err
	}
	client, err := api.ForPortal(portal)
	if err != nil {
		return nil, nil, err
	}
	return d, client, nil
}
