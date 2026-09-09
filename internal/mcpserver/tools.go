package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/auth"
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

	addTool(s, &mcp.Tool{Name: "version", Description: "Print the cc-data binary version.", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in struct{}) (*mcp.CallToolResult, versionOut, error) {
			return nil, versionOut{Version: opts.Version}, nil
		})

	addTool(s, &mcp.Tool{Name: "auth_status", Description: "List portals with stored credentials. Set check=true to validate each token over the network (an opt-in per-portal call).", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in authStatusIn) (*mcp.CallToolResult, auth.StatusResult, error) {
			res, err := auth.Status(ctx, in.Check)
			return nil, res, err
		})

	addTool(s, &mcp.Tool{Name: "reports_list", Description: "List the user's report runs for a portal. The portal may be a hostname or an environment alias (prod / staging / dev).", Annotations: readOnly},
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

	addTool(s, &mcp.Tool{Name: "reports_filter_options", Description: "List the values a report filter dimension offers the user, narrowed by any selections already made, so a filter can be assembled without the web form. Also answers \"what data can I see?\" on its own. Pass report_filter to narrow (the same object reports_list returns on a run), search to match labels, and report_slug to restrict to a report that offers the dimension. Set include_count=true when the user asks how many there are, and all=true to walk the pages, which stops after 1000 options and sets truncated with next_page_token to continue from. The portal may be a hostname or an environment alias (prod / staging / dev).", Annotations: readOnly},
		func(ctx context.Context, req *mcp.CallToolRequest, in reportsFilterOptionsIn) (*mcp.CallToolResult, reportview.FilterOptionsPayload, error) {
			client, err := portalClient(in.Portal)
			if err != nil {
				return nil, reportview.FilterOptionsPayload{}, err
			}
			filter, err := encodeReportFilter(in.ReportFilter)
			if err != nil {
				return nil, reportview.FilterOptionsPayload{}, err
			}
			optReq := api.FilterOptionsReq{
				Dimension:    in.Dimension,
				ReportSlug:   in.ReportSlug,
				Search:       in.Search,
				Limit:        in.Limit,
				PageToken:    in.PageToken,
				IncludeCount: in.IncludeCount,
				ReportFilter: filter,
			}
			page, err := client.FilterOptionsFor(ctx, optReq, in.All)
			if err != nil {
				return nil, reportview.FilterOptionsPayload{}, api.AsCLIError(err)
			}
			return nil, reportview.FilterOptions(page), nil
		})

	addTool(s, &mcp.Tool{Name: "reports_jobs", Description: "List a run's post-processing jobs. The portal may be a hostname or an environment alias (prod / staging / dev).", Annotations: readOnly},
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

	addTool(s, &mcp.Tool{Name: "reports_create", Description: "Create a report run from a report slug and a filter, without the web form. Pass report_filter as the same object reports_list returns on a run, assembled with reports_filter_options. The server derives the run's filter labels, forces hide_names by the user's role, and refuses an id the user cannot see. The new run is returned in the shape reports_list uses; an Athena run's query starts on its own, so its state may be null until it is read. The portal may be a hostname or an environment alias (prod / staging / dev)."},
		func(ctx context.Context, req *mcp.CallToolRequest, in reportsCreateIn) (*mcp.CallToolResult, reportview.RunPayload, error) {
			client, err := portalClient(in.Portal)
			if err != nil {
				return nil, reportview.RunPayload{}, err
			}
			filter, err := encodeReportFilter(in.ReportFilter)
			if err != nil {
				return nil, reportview.RunPayload{}, err
			}
			run, err := client.CreateReport(ctx, api.CreateReportReq{ReportSlug: in.ReportSlug, ReportFilter: filter})
			if err != nil {
				return nil, reportview.RunPayload{}, api.AsWriteCLIError(err, api.RunMayExistAction)
			}
			return nil, reportview.RunPayload{Run: reportview.ToRunJSON(run)}, nil
		})

	addTool(s, &mcp.Tool{Name: "reports_duplicate", Description: "Take a fresh snapshot of an existing run, by creating a new run from its report and filter. A Portal report is computed live on every request, so re-read one with get_report rather than duplicating it; duplicating a Portal run is refused unless force is set. An Athena run is frozen once it finishes, so duplicating is how it is re-run. The portal may be a hostname or an environment alias (prod / staging / dev)."},
		func(ctx context.Context, req *mcp.CallToolRequest, in reportsDuplicateIn) (*mcp.CallToolResult, reportview.RunPayload, error) {
			client, err := portalClient(in.Portal)
			if err != nil {
				return nil, reportview.RunPayload{}, err
			}
			run, err := client.DuplicateReport(ctx, in.RunID, in.Force)
			if err != nil {
				return nil, reportview.RunPayload{}, api.AsWriteCLIError(err, api.RunMayExistAction)
			}
			return nil, reportview.RunPayload{Run: reportview.ToRunJSON(run)}, nil
		})

	addTool(s, &mcp.Tool{Name: "get_report", Description: "Download a report CSV into a dataset."},
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

	addTool(s, &mcp.Tool{Name: "get_answers", Description: "Download a run's student answers into a dataset."},
		pagedHandler(store.TypeAnswers))
	addTool(s, &mcp.Tool{Name: "get_history", Description: "Download a run's interactive state history into a dataset."},
		pagedHandler(store.TypeHistory))

	addTool(s, &mcp.Tool{Name: "get_attachments", Description: "Download a run's file attachments into a dataset. Fetch that run's answers or history first: attachments are reached through those records. " + noArgsMsg},
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

	addTool(s, &mcp.Tool{Name: "dataset_create", Description: "Create a new, empty dataset to pull runs into. The portal is optional and falls back to the configured default portal. It takes a hostname only and refuses an environment alias (prod / staging / dev), unlike the portal on reports_list and reports_jobs, because it also names the folder the data lives in."},
		func(ctx context.Context, req *mcp.CallToolRequest, in datasetCreateIn) (*mcp.CallToolResult, mapOut, error) {
			cfg, root, err := loadRuntime()
			if err != nil {
				return nil, nil, err
			}
			if err := checkCreateArgs(in.Portal, in.Name); err != nil {
				return nil, nil, err
			}
			raw := in.Name
			if in.Portal != "" {
				raw = in.Portal + "/" + in.Name
			}
			ref, err := dataset.ParseRefForConfig(cfg, raw)
			if err != nil {
				return nil, nil, err
			}
			if _, err := dataset.Create(root, ref, in.Description); err != nil {
				return nil, nil, err
			}
			return nil, mapOut{"ref": ref.String(), "created": true}, nil
		})

	addTool(s, &mcp.Tool{Name: "dataset_list", Description: "List datasets across all portals.", Annotations: readOnly},
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

	addTool(s, &mcp.Tool{Name: "dataset_show", Description: "Show a dataset's holdings and warnings.", Annotations: readOnly},
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

	addTool(s, &mcp.Tool{Name: "dataset_rename", Description: "Rename a dataset."},
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

	addTool(s, &mcp.Tool{Name: "dataset_edit", Description: "Edit a dataset's description."},
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

	addTool(s, &mcp.Tool{Name: "dataset_delete", Description: "Permanently delete a dataset: its folder, its manifest, and every report, answer, history and attachment file downloaded into it. This cannot be undone and nothing is moved to a trash folder; the runs would have to be fetched again into a new dataset. Requires confirm:true.", Annotations: destructive},
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

	addTool(s, &mcp.Tool{Name: "dataset_purge", Description: "Permanently delete every file downloaded into a dataset, keeping the dataset itself, its name and its description. This cannot be undone; the runs would have to be fetched again. Requires confirm:true.", Annotations: destructive},
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

	addTool(s, &mcp.Tool{Name: "dataset_reindex", Description: "Rebuild a dataset's manifest from the filesystem."},
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

	addTool(s, &mcp.Tool{Name: "query", Description: queryDescription(), Annotations: readOnly},
		queryHandler(opts))
}

// checkCreateArgs validates portal and name separately so a failure names the argument at
// fault. Joining them first and letting the ref parser split them again reports a slash in
// the portal as a bad name. A scheme is stripped before the path check, since a URL-shaped
// portal is accepted and normalized to its hostname.
func checkCreateArgs(portal, name string) error {
	if name == "" {
		return fmt.Errorf("name is required: the dataset name, without a portal")
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("dataset name %q must not contain a slash; pass the portal in the portal argument", name)
	}
	host := portal
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if strings.Contains(host, "/") {
		return fmt.Errorf("portal %q must be a hostname with no path; pass the dataset name in the name argument", portal)
	}
	return nil
}

// queryDescription names the views from the registration rather than restating them, so it
// cannot drift from what a dataset actually exposes. The per-run and per-job view shapes are
// left to the guidance, which every client of this tool also receives: written here they
// would be a view-name copy in the one place the drift guard cannot read.
func queryDescription() string {
	return "Run SQL over one or more datasets. Each datasets entry may be alias=ref to schema-qualify that dataset. " +
		"Always-present views: " + strings.Join(duck.StaticViewNames(), ", ") + ". " +
		"res_<N>_<question_id>_answer columns are VARCHAR and hold prompt text on pseudo-header rows, so aggregate them numerically with TRY_CAST. " +
		"Cross-dataset unions are never implicit: write them with UNION ALL BY NAME. " +
		"Rows beyond max_rows (default 1000) are dropped and truncated is set. " + noArgsMsg
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

// portalClient resolves a portal argument to an authenticated client, accepting
// the same environment aliases and hostnames the CLI's --portal does, including
// a portal only a stored credential vouches for.
func portalClient(portal string) (*api.Client, error) {
	host, _, err := auth.ResolvePortalTarget(portal)
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
			ref, perr := dataset.ParseRefForExisting(cfg, root, refStr)
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

// encodeReportFilter turns a decoded filter object back into the raw JSON the client passes
// through. The tools take it decoded because json.RawMessage reflects to a byte array in the
// argument schema and refuses the object reports_list hands back.
func encodeReportFilter(filter map[string]any) (json.RawMessage, error) {
	if len(filter) == 0 {
		return nil, nil
	}
	raw, err := json.Marshal(filter)
	if err != nil {
		return nil, fmt.Errorf("report_filter is not encodable: %w", err)
	}
	return raw, nil
}
