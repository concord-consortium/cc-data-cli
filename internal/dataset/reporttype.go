package dataset

import "sort"

// Report type vocabulary.
const (
	ReportTypeAnswers   = "answers"
	ReportTypeUsage     = "usage"
	ReportTypeLog       = "log"
	ReportTypeRecovered = "recovered"
	// ReportTypePortal covers every report the portal computes on request. It is one type for
	// all of them rather than one per report, because report_type decides union membership and
	// the answers pseudo-header split, neither of which distinguishes a mapping report from a
	// metrics one; anyone naming the report reads the download's slug.
	ReportTypePortal = "portal"
)

// slugToType is cc-data's own copy of the Athena slugs, not a roster of what the server
// offers. Nothing reconciles it: an unrecognized slug degrades with "unknown to this
// cc-data version" rather than failing, and the guidance guard can only prove the guidance
// matches this map, never that this map matches the server. REPORT-130 replaces the copy
// with a catalog read at runtime.
var slugToType = map[string]string{
	"student-answers":               ReportTypeAnswers,
	"student-assignment-usage":      ReportTypeUsage,
	"student-actions":               ReportTypeLog,
	"student-actions-with-metadata": ReportTypeLog,
	"teacher-actions":               ReportTypeLog,
}

// ReportTypeFromSlug derives the report type from a known slug; ok is false for
// an unrecognized slug (recorded verbatim and quarantined at query time).
func ReportTypeFromSlug(slug string) (string, bool) {
	t, ok := slugToType[slug]
	return t, ok
}

// allowedReportTypes is the reports-union allowlist, in one place so the guidance guard
// and the predicate cannot disagree about what the vocabulary is.
var allowedReportTypes = []string{
	ReportTypeAnswers, ReportTypeUsage, ReportTypeLog, ReportTypePortal, ReportTypeRecovered,
}

// AllowedReportTypes returns the report types the reports union admits. portal and
// recovered are assigned locally rather than arriving on a run: portal is derived from a
// run's execution, recovered from a reindex that cannot classify a CSV.
func AllowedReportTypes() []string {
	return append([]string(nil), allowedReportTypes...)
}

// RunReportTypes returns what the server sends as a run's report_type. A Portal run sends
// none, which is why execution and not this list identifies one.
func RunReportTypes() []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range slugToType {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	sort.Strings(out)
	return out
}

// IsAllowedReportType reports whether a report type is in the reports-union allowlist.
func IsAllowedReportType(t string) bool {
	for _, a := range allowedReportTypes {
		if t == a {
			return true
		}
	}
	return false
}

// ReportSlugs returns the Athena slugs cc-data knows.
func ReportSlugs() []string {
	var out []string
	for slug := range slugToType {
		out = append(out, slug)
	}
	sort.Strings(out)
	return out
}
