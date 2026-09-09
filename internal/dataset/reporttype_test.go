package dataset

import "testing"

func TestIsAllowedReportType(t *testing.T) {
	for _, allowed := range []string{ReportTypeAnswers, ReportTypeUsage, ReportTypeLog, ReportTypePortal, ReportTypeRecovered} {
		if !IsAllowedReportType(allowed) {
			t.Errorf("%q should be in the reports-union allowlist", allowed)
		}
	}
	// An unknown type is recorded verbatim and quarantined at query time, which is what a report
	// slug this version has never heard of falls back to.
	for _, refused := range []string{"", "student-id-mapping", "mapping"} {
		if IsAllowedReportType(refused) {
			t.Errorf("%q should not be in the reports-union allowlist", refused)
		}
	}
}

func TestReportTypeFromSlug(t *testing.T) {
	for slug, want := range map[string]string{
		"student-answers":               ReportTypeAnswers,
		"student-assignment-usage":      ReportTypeUsage,
		"student-actions":               ReportTypeLog,
		"student-actions-with-metadata": ReportTypeLog,
		"teacher-actions":               ReportTypeLog,
	} {
		if got, ok := ReportTypeFromSlug(slug); !ok || got != want {
			t.Errorf("ReportTypeFromSlug(%q) = %q, %v; want %q, true", slug, got, ok, want)
		}
	}
	// The Portal slugs are deliberately absent: their type comes from the run's execution, so a
	// Portal report added to the server later needs no entry here.
	for _, slug := range []string{"student-id-mapping", "student-metadata", "teacher-status"} {
		if got, ok := ReportTypeFromSlug(slug); ok {
			t.Errorf("ReportTypeFromSlug(%q) = %q, true; want no mapping", slug, got)
		}
	}
}
