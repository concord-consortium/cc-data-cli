package dataset

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

// IsAllowedReportType reports whether a report type is in the reports-union
// allowlist (answers/usage/log/portal, plus the reindex-only recovered value).
func IsAllowedReportType(t string) bool {
	switch t {
	case ReportTypeAnswers, ReportTypeUsage, ReportTypeLog, ReportTypePortal, ReportTypeRecovered:
		return true
	}
	return false
}
