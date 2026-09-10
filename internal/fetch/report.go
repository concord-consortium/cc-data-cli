// Package fetch implements the download commands: report CSVs, paged
// answers/history, and attachments.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/api"
	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/concord-consortium/cc-data-cli/internal/reportview"
	"github.com/concord-consortium/cc-data-cli/internal/store"
)

const (
	typeReport    = "report"
	typeReportJob = "report_job"
)

// DefaultPollTimeout bounds report polling (sized against Athena's DML timeout).
const DefaultPollTimeout = 30 * time.Minute

// ReportOptions parameterizes a report CSV fetch.
type ReportOptions struct {
	DS          *dataset.Dataset
	Client      *api.Client
	RunID       int
	JobID       *int
	NoWait      bool
	Refresh     bool
	PollTimeout time.Duration
	Progress    io.Writer
}

// FetchReport downloads a run's CSV. A sync run's body is computed and streamed by the API; an
// async run polls, follows the presigned envelope and streams from S3. Everything after that fork
// is shared. It returns the result-line value (emitted by the caller) and an error carrying the
// exit code.
func FetchReport(ctx context.Context, opts ReportOptions) (any, error) {
	if opts.PollTimeout == 0 {
		opts.PollTimeout = DefaultPollTimeout
	}
	release, err := acquireDownload(opts.DS, reportLockName(opts.RunID, opts.JobID), reportBusyMsg(opts))
	if err != nil {
		return nil, err
	}
	defer release()

	csvName := reportCSVName(opts.RunID, opts.JobID)
	csvPath := opts.DS.Path(csvName)

	run, err := opts.Client.GetReport(ctx, opts.RunID)
	if err != nil {
		return nil, api.AsCLIError(err)
	}
	isSync := run.Execution == api.ExecutionSync

	// A job is a separate resource on a separate route and a Portal report has none, so passing
	// one through would store the run's own CSV under a job's name, type and view.
	if isSync && opts.JobID != nil {
		return nil, output.Usagef("run %d is a Portal report, which has no jobs; drop --job", opts.RunID)
	}
	if fileExists(csvPath) && !opts.Refresh {
		return nil, &output.CLIError{ExitCode: output.ExitUsage, Code: "EXISTS", Message: existsMsg(csvName, isSync)}
	}
	reportType := resolveReportType(run, opts.Progress)

	tmpPath := csvPath + ".tmp"
	if isSync {
		if err := opts.Client.StreamReportCSV(ctx, opts.RunID, tmpPath); err != nil {
			os.Remove(tmpPath)
			return nil, api.AsCLIError(err)
		}
	} else {
		env, notReady, cliErr := pollUntilReady(ctx, opts)
		if cliErr != nil {
			return notReady, cliErr
		}
		if err := streamReady(ctx, opts, env, tmpPath); err != nil {
			os.Remove(tmpPath)
			return nil, api.AsCLIError(err)
		}
	}

	rowCount, columns, columnOrder, dialect, err := dataset.DetectCSV(tmpPath, reportType)
	if err != nil {
		os.Remove(tmpPath)
		return nil, output.Internalf("detecting CSV columns: %v", err)
	}
	if err := os.Rename(tmpPath, csvPath); err != nil {
		os.Remove(tmpPath)
		return nil, output.Internalf("finalizing CSV: %v", err)
	}

	dlType := typeReport
	if opts.JobID != nil {
		dlType = typeReportJob
	}
	entry := dataset.Download{
		Type:         dlType,
		RunID:        opts.RunID,
		JobID:        opts.JobID,
		Slug:         run.ReportSlug,
		ReportType:   reportType,
		Filters:      run.ReportFilter,
		FilterLabels: reportview.FilterLabels(*run),
		Files:        []string{csvName},
		RowCount:     &rowCount,
		Columns:      columns,
		ColumnOrder:  columnOrder,
		CSVDialect:   &dialect,
		Complete:     true,
		FetchedAt:    time.Now().UTC(),
	}
	if err := opts.DS.UpsertDownload(entry); err != nil {
		return nil, output.Internalf("recording download: %v", err)
	}

	result := map[string]any{
		"type":        dlType,
		"run_id":      opts.RunID,
		"files":       []string{csvName},
		"report_type": reportType,
		"row_count":   rowCount,
		"complete":    true,
	}
	if opts.JobID != nil {
		result["job_id"] = *opts.JobID
	}
	return result, nil
}

// existsMsg refuses an already-downloaded run. A Portal report is recomputed on every request, so
// re-downloading is the normal way to get current data rather than a redundant act, and the
// server's duplicate guard sends callers here to do exactly that.
func existsMsg(csvName string, isSync bool) string {
	if isSync {
		return fmt.Sprintf("%s already exists; Portal reports are live, so use --refresh to re-pull current data", csvName)
	}
	return fmt.Sprintf("%s already exists; use --refresh to re-download", csvName)
}

// resolveReportType uses the server value when present, else derives from the
// slug, warning on an unknown type.
func resolveReportType(run *api.ReportRun, progress io.Writer) string {
	if run.ReportType != nil && *run.ReportType != "" {
		if !dataset.IsAllowedReportType(*run.ReportType) {
			fmt.Fprintf(progress, "warning: report_type %q is unknown to this cc-data version; it will be excluded from the reports view. Consider upgrading.\n", *run.ReportType)
		}
		return *run.ReportType
	}
	// A Portal report declares no api_report_type on the wire by design. Deriving the type from
	// the execution rather than from a slug map means a Portal report added to the server later
	// is recognized without a cc-data release.
	if run.Execution == api.ExecutionSync {
		return dataset.ReportTypePortal
	}
	if t, ok := dataset.ReportTypeFromSlug(run.ReportSlug); ok {
		return t
	}
	fmt.Fprintf(progress, "warning: report slug %q is unknown to this cc-data version; its type will be excluded from the reports view. Consider upgrading.\n", run.ReportSlug)
	return run.ReportSlug
}

// pollUntilReady polls the download endpoint until it is ready or a terminal
// condition applies. On not-ready-with-no-wait or budget expiry it returns a
// result-line value plus a silent exit-4 error.
func pollUntilReady(ctx context.Context, opts ReportOptions) (*api.DownloadEnvelope, any, *output.CLIError) {
	isJob := opts.JobID != nil
	deadline := time.Now().Add(opts.PollTimeout)
	backoff := 2 * time.Second
	var osc oscillation

	for {
		env, err := opts.Client.ReportDownloadEnvelope(ctx, opts.RunID, opts.JobID)
		if err == nil {
			return env, nil, nil
		}
		var apiErr *api.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != api.CodeNotReady {
			return nil, nil, api.AsCLIError(err)
		}
		state := extractState(apiErr, isJob)

		if isTerminalFailure(state, isJob) {
			// The NOT_READY body is a caller-visible contract, pinned server-side, so it is
			// forwarded whole: a field the server adds reaches the envelope with no client release.
			return nil, nil, &output.CLIError{
				ExitCode: output.ExitContract,
				Code:     api.CodeNotReady,
				Message:  fmt.Sprintf("run %d is in terminal state %q; nothing to download", opts.RunID, state),
				Extra:    apiErr.Extra,
			}
		}
		if opts.NoWait {
			return nil, notReadyResult(opts, state), &output.CLIError{ExitCode: output.ExitNotReady, Silent: true}
		}
		if osc.observe(state) {
			return nil, nil, &output.CLIError{
				ExitCode: output.ExitContract,
				Code:     api.CodeNotReady,
				Message:  fmt.Sprintf("the server repeatedly failed to start the query for run %d", opts.RunID),
			}
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(opts.Progress, "run %d still not ready after %s (last state %q)\n", opts.RunID, opts.PollTimeout, state)
			return nil, notReadyResult(opts, state), &output.CLIError{ExitCode: output.ExitNotReady, Silent: true}
		}
		fmt.Fprintf(opts.Progress, "run %d state=%q; waiting...\n", opts.RunID, state)
		// Cap the sleep so a long backoff cannot overshoot the poll deadline.
		sleep := jitter(backoff)
		if remaining := time.Until(deadline); sleep > remaining {
			sleep = remaining
		}
		if !pollSleep(ctx, sleep) {
			return nil, nil, &output.CLIError{ExitCode: output.ExitInternal, Code: "CANCELLED", Message: ctx.Err().Error()}
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func streamReady(ctx context.Context, opts ReportOptions, env *api.DownloadEnvelope, tmpPath string) error {
	if err := opts.Client.DownloadURL(ctx, env.DownloadURL, tmpPath); err == nil {
		return nil
	}
	// The presigned URL failed (expiry, transient S3); re-mint fresh envelopes
	// within the bounded retry budget.
	_, err := opts.Client.StreamToFile(ctx, func(ctx context.Context) (*api.DownloadEnvelope, error) {
		return opts.Client.ReportDownloadEnvelope(ctx, opts.RunID, opts.JobID)
	}, tmpPath)
	return err
}

// oscillation detects the server's null->queued->null self-start-failure cycle.
type oscillation struct {
	prev            string
	nullAfterQueued int
}

func (o *oscillation) observe(state string) bool {
	if o.prev == "queued" && state == "" {
		o.nullAfterQueued++
	}
	o.prev = state
	return o.nullAfterQueued >= 2
}

func extractState(apiErr *api.APIError, isJob bool) string {
	key := "athena_query_state"
	if isJob {
		key = "status"
	}
	if v, ok := apiErr.Extra[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func isTerminalFailure(state string, isJob bool) bool {
	if isJob {
		return state == "failed"
	}
	return state == "failed" || state == "cancelled"
}

func notReadyResult(opts ReportOptions, state string) map[string]any {
	dlType := typeReport
	stateKey := "athena_query_state"
	if opts.JobID != nil {
		dlType = typeReportJob
		stateKey = "status"
	}
	result := map[string]any{
		"type":     dlType,
		"run_id":   opts.RunID,
		"complete": false,
		stateKey:   state,
	}
	if opts.JobID != nil {
		result["job_id"] = *opts.JobID
	}
	return result
}

func reportLockName(runID int, jobID *int) string {
	if jobID != nil {
		return fmt.Sprintf("seg_report_job_%d_%d.lock", runID, *jobID)
	}
	return fmt.Sprintf("seg_report_%d.lock", runID)
}

func reportCSVName(runID int, jobID *int) string {
	if jobID != nil {
		return fmt.Sprintf("report_%d_job_%d.csv", runID, *jobID)
	}
	return fmt.Sprintf("report_%d.csv", runID)
}

func reportBusyMsg(opts ReportOptions) string {
	if opts.JobID != nil {
		return fmt.Sprintf("download busy: another cc-data command is fetching run %d job %d", opts.RunID, *opts.JobID)
	}
	return fmt.Sprintf("download busy: another cc-data command is fetching run %d report", opts.RunID)
}

// acquireDownload takes the per-download lock plus the shared activity lock for
// the command's lifetime.
func acquireDownload(ds *dataset.Dataset, lockName, busyMsg string) (func(), error) {
	act := ds.Activity()
	aok, err := act.TryRLock()
	if err != nil {
		return nil, output.Internalf("activity lock: %v", err)
	}
	if !aok {
		return nil, output.Busyf("dataset is busy: another cc-data command is writing to it")
	}
	dl := store.DownloadLockFor(ds.Dir, lockName)
	ok, err := dl.TryLock()
	if err != nil {
		act.RUnlock()
		return nil, output.Internalf("download lock: %v", err)
	}
	if !ok {
		act.RUnlock()
		return nil, output.Busyf("%s", busyMsg)
	}
	return func() {
		dl.Unlock()
		act.RUnlock()
	}, nil
}

// pollSleep is a seam so tests can poll without wall-clock delay.
var pollSleep = sleepCtx

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func jitter(d time.Duration) time.Duration {
	return time.Duration(rand.Float64() * float64(d))
}
