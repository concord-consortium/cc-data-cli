package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/auth"
	"github.com/concord-consortium/cc-data-cli/internal/config"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/duck"
	"github.com/concord-consortium/cc-data-cli/internal/fetch"
	"github.com/concord-consortium/cc-data-cli/internal/reportview"
	"github.com/concord-consortium/cc-data-cli/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const noArgsMsg = "Note: url/inline and allow-dir arguments are intentionally excluded from MCP tools; they mint capabilities or widen the sandbox. Use cc-data mcp --allow-dir launch args to extend the query sandbox."

// registerTools registers the pinned data-and-analysis surface. Excluded by
// design: login/logout (credential management is a terminal act), repl
// (interactive), mcp (recursive), init/uninstall (host-machine installer acts).
func registerTools(s *mcp.Server, opts Options) {
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}
	destructive := &mcp.ToolAnnotations{DestructiveHint: ptr(true)}

	mcp.AddTool(s, &mcp.Tool{Name: "version", Description: "Print the cc-data binary version.", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, versionOut, error) {
			return nil, versionOut{Version: opts.Version}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "auth_status", Description: "List portals with stored credentials. Set check=true to validate each token over the network (an opt-in per-portal call).", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in authStatusIn) (*mcp.CallToolResult, auth.StatusResult, error) {
			res, err := auth.Status(ctx, in.Check)
			return nil, res, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reports_list", Description: "List the user's report runs for a portal.", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in portalIn) (*mcp.CallToolResult, reportview.RunsPayload, error) {
			client, err := portalClient(in.Portal)
			if err != nil {
				return nil, reportview.RunsPayload{}, err
			}
			runs, err := client.ListReports(ctx)
			if err != nil {
				return nil, reportview.RunsPayload{}, api.AsCLIError(err)
			}
			return nil, reportview.Runs(runs), nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "reports_jobs", Description: "List a run's post-processing jobs.", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in reportsJobsIn) (*mcp.CallToolResult, reportview.JobsPayload, error) {
			client, err := portalClient(in.Portal)
			if err != nil {
				return nil, reportview.JobsPayload{}, err
			}
			jobs, err := client.ListJobs(ctx, in.RunID)
			if err != nil {
				return nil, reportview.JobsPayload{}, api.AsCLIError(err)
			}
			return nil, reportview.JobsPayload{Jobs: jobs}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_report", Description: "Download a report CSV into a dataset."},
		func(ctx context.Context, req *mcp.CallToolRequest, in getReportIn) (*mcp.CallToolResult, mapOut, error) {
			d, client, err := openForFetch(in.Dataset)
			if err != nil {
				return nil, nil, err
			}
			o := fetch.ReportOptions{DS: d, Client: client, RunID: in.RunID, NoWait: in.NoWait, Refresh: in.Refresh, Progress: newProgress(ctx, req)}
			if in.Job != 0 {
				o.JobID = &in.Job
			}
			return fetchResult(fetch.FetchReport(ctx, o))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_answers", Description: "Download a run's student answers into a dataset."},
		pagedHandler(store.TypeAnswers))
	mcp.AddTool(s, &mcp.Tool{Name: "get_history", Description: "Download a run's interactive state history into a dataset."},
		pagedHandler(store.TypeHistory))

	mcp.AddTool(s, &mcp.Tool{Name: "get_attachments", Description: "Download a run's file attachments into a dataset. " + noArgsMsg},
		func(ctx context.Context, req *mcp.CallToolRequest, in getAttachmentsIn) (*mcp.CallToolResult, mapOut, error) {
			d, client, err := openForFetch(in.Dataset)
			if err != nil {
				return nil, nil, err
			}
			// url/inline are intentionally not exposed over MCP.
			o := fetch.AttachmentOptions{DS: d, Client: client, RunID: in.RunID, Refresh: in.Refresh,
				Answer: in.Answer, History: in.History, Question: in.Question, Name: in.Name, Progress: newProgress(ctx, req)}
			return fetchResult(fetch.FetchAttachments(ctx, o))
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_create", Description: "Create a new dataset."},
		func(ctx context.Context, req *mcp.CallToolRequest, in datasetCreateIn) (*mcp.CallToolResult, mapOut, error) {
			cfg, root, err := loadRuntime()
			if err != nil {
				return nil, nil, err
			}
			ref, err := dataset.ParseRef(in.Ref, cfg.DefaultPortal)
			if err != nil {
				return nil, nil, err
			}
			if _, err := dataset.Create(root, ref, in.Description); err != nil {
				return nil, nil, err
			}
			return nil, mapOut{"ref": ref.String(), "created": true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_list", Description: "List datasets across all portals.", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, dataset.ListJSON, error) {
			_, root, err := loadRuntime()
			if err != nil {
				return nil, dataset.ListJSON{}, err
			}
			list, err := dataset.BuildListJSON(root)
			if err != nil {
				return nil, dataset.ListJSON{}, err
			}
			return nil, *list, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_show", Description: "Show a dataset's holdings and warnings.", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in datasetShowIn) (*mcp.CallToolResult, dataset.ShowJSON, error) {
			d, _, err := openDataset(in.Ref)
			if err != nil {
				return nil, dataset.ShowJSON{}, err
			}
			s, err := d.BuildShowJSON(in.Full)
			if err != nil {
				return nil, dataset.ShowJSON{}, err
			}
			return nil, *s, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_rename", Description: "Rename a dataset."},
		func(ctx context.Context, req *mcp.CallToolRequest, in datasetRenameIn) (*mcp.CallToolResult, mapOut, error) {
			cfg, root, err := loadRuntime()
			if err != nil {
				return nil, nil, err
			}
			d, _, err := openDataset(in.Ref)
			if err != nil {
				return nil, nil, err
			}
			newD, err := d.Rename(root, in.NewName)
			if err != nil {
				return nil, nil, err
			}
			_ = cfg
			return nil, mapOut{"ref": newD.Ref.String(), "renamed": true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_edit", Description: "Edit a dataset's description."},
		func(ctx context.Context, req *mcp.CallToolRequest, in datasetEditIn) (*mcp.CallToolResult, mapOut, error) {
			d, _, err := openDataset(in.Ref)
			if err != nil {
				return nil, nil, err
			}
			if err := d.Edit(in.Description); err != nil {
				return nil, nil, err
			}
			return nil, mapOut{"ref": d.Ref.String(), "edited": true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_delete", Description: "Delete a dataset folder (requires confirm:true).", Annotations: destructive},
		func(ctx context.Context, req *mcp.CallToolRequest, in confirmRefIn) (*mcp.CallToolResult, mapOut, error) {
			if !in.Confirm {
				return nil, nil, fmt.Errorf("dataset_delete requires confirm:true")
			}
			d, _, err := openDataset(in.Ref)
			if err != nil {
				return nil, nil, err
			}
			if err := d.Delete(); err != nil {
				return nil, nil, err
			}
			return nil, mapOut{"ref": d.Ref.String(), "deleted": true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_purge", Description: "Delete a dataset's downloaded data but keep the dataset (requires confirm:true).", Annotations: destructive},
		func(ctx context.Context, req *mcp.CallToolRequest, in confirmRefIn) (*mcp.CallToolResult, mapOut, error) {
			if !in.Confirm {
				return nil, nil, fmt.Errorf("dataset_purge requires confirm:true")
			}
			d, _, err := openDataset(in.Ref)
			if err != nil {
				return nil, nil, err
			}
			if err := d.Purge(); err != nil {
				return nil, nil, err
			}
			return nil, mapOut{"ref": d.Ref.String(), "purged": true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "dataset_reindex", Description: "Rebuild a dataset's manifest from the filesystem."},
		func(ctx context.Context, req *mcp.CallToolRequest, in datasetRefIn) (*mcp.CallToolResult, mapOut, error) {
			d, _, err := openDataset(in.Ref)
			if err != nil {
				return nil, nil, err
			}
			if err := d.Reindex(); err != nil {
				return nil, nil, err
			}
			return nil, mapOut{"ref": d.Ref.String(), "reindexed": true}, nil
		})

	mcp.AddTool(s, &mcp.Tool{Name: "query", Description: "Run SQL over one or more datasets. Each datasets entry may be alias=ref to schema-qualify that dataset. Rows beyond max_rows (default 1000) are dropped and truncated is set. " + noArgsMsg, Annotations: readOnly},
		queryHandler(opts))
}

func pagedHandler(typ string) func(context.Context, *mcp.CallToolRequest, getPagedIn) (*mcp.CallToolResult, mapOut, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in getPagedIn) (*mcp.CallToolResult, mapOut, error) {
		d, client, err := openForFetch(in.Dataset)
		if err != nil {
			return nil, nil, err
		}
		o := fetch.PagedOptions{DS: d, Client: client, RunID: in.RunID, Type: typ, Refresh: in.Refresh, Progress: newProgress(ctx, req)}
		return fetchResult(fetch.FetchPaged(ctx, o))
	}
}

// fetchResult returns the fetch result payload when present (including a
// not-ready result), otherwise the error.
func fetchResult(result any, err error) (*mcp.CallToolResult, mapOut, error) {
	if m, ok := result.(map[string]any); ok {
		return nil, mapOut(m), nil
	}
	if err != nil {
		return nil, nil, err
	}
	return nil, mapOut{}, nil
}

func portalClient(portal string) (*api.Client, error) {
	host, err := config.NormalizePortal(portal)
	if err != nil {
		return nil, err
	}
	return api.ForPortal(host)
}

func queryHandler(opts Options) func(context.Context, *mcp.CallToolRequest, queryIn) (*mcp.CallToolResult, queryOut, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in queryIn) (*mcp.CallToolResult, queryOut, error) {
		if len(in.Datasets) == 0 {
			return nil, queryOut{}, fmt.Errorf("at least one dataset is required")
		}
		cfg, root, err := loadRuntime()
		if err != nil {
			return nil, queryOut{}, err
		}
		var specs []duck.DatasetSpec
		for _, raw := range in.Datasets {
			// Mirror the CLI's alias=ref split (cmd/query.go); an entry without
			// '=' keeps an empty alias.
			alias := ""
			refStr := raw
			if i := strings.Index(raw, "="); i >= 0 {
				alias, refStr = raw[:i], raw[i+1:]
			}
			ref, perr := dataset.ParseRef(refStr, cfg.DefaultPortal)
			if perr != nil {
				return nil, queryOut{}, perr
			}
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return nil, queryOut{}, fmt.Errorf("dataset %s does not exist", ref)
			}
			specs = append(specs, duck.DatasetSpec{Alias: alias, DS: d})
		}
		maxRows := in.MaxRows
		if maxRows <= 0 {
			maxRows = 1000
		}
		e, err := duck.Open(ctx, specs, opts.AllowDirs, newProgress(ctx, req))
		if err != nil {
			return nil, queryOut{}, err
		}
		defer e.Close()
		return runQuery(ctx, e, in.SQL, maxRows)
	}
}
