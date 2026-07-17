package dataset

import (
	"encoding/csv"
	"io"
	"os"
	"strconv"
	"strings"
)

// DefaultCSVDialect is the fixed RFC4180 dialect report CSVs use.
func DefaultCSVDialect() CSVDialect {
	return CSVDialect{Delim: ",", Quote: `"`, Escape: `"`, Header: true}
}

// DetectCSV makes a single full-file pass over a report CSV, counting data rows
// and detecting each column's DuckDB type by widening over every value. For an
// answers-type CSV the two pseudo-header rows (student_id in Prompt/Correct
// answer) are excluded from row_count but included in type detection, since they
// are real rows DuckDB reads.
func DetectCSV(path, reportType string) (rowCount int, columns map[string]string, dialect CSVDialect, err error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil, CSVDialect{}, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true

	header, err := r.Read()
	if err != nil {
		if err == io.EOF {
			return 0, map[string]string{}, DefaultCSVDialect(), nil
		}
		return 0, nil, CSVDialect{}, err
	}
	cols := make([]string, len(header))
	copy(cols, header)
	studentIDCol := indexOf(cols, "student_id")
	types := make([]string, len(cols))

	isAnswers := reportType == ReportTypeAnswers
	for {
		row, rerr := r.Read()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, nil, CSVDialect{}, rerr
		}
		pseudoHeader := isAnswers && studentIDCol >= 0 && studentIDCol < len(row) &&
			(row[studentIDCol] == "Prompt" || row[studentIDCol] == "Correct answer")
		if !pseudoHeader {
			rowCount++
		}
		for i := 0; i < len(cols) && i < len(row); i++ {
			types[i] = widenCSV(types[i], row[i])
		}
	}

	columns = make(map[string]string, len(cols))
	for i, name := range cols {
		t := types[i]
		if t == "" {
			t = TypeVARCHAR
		}
		columns[name] = t
	}
	return rowCount, columns, DefaultCSVDialect(), nil
}

// CSV column types (a subset of the store types; no JSON in CSV).
const (
	TypeBIGINT  = "BIGINT"
	TypeDOUBLE  = "DOUBLE"
	TypeBOOLEAN = "BOOLEAN"
	TypeVARCHAR = "VARCHAR"
)

func widenCSV(cur, val string) string {
	if val == "" {
		return cur
	}
	next := csvValueType(val)
	if cur == "" {
		return next
	}
	if cur == next {
		return cur
	}
	if (cur == TypeBIGINT && next == TypeDOUBLE) || (cur == TypeDOUBLE && next == TypeBIGINT) {
		return TypeDOUBLE
	}
	return TypeVARCHAR
}

func csvValueType(val string) string {
	if _, err := strconv.ParseInt(val, 10, 64); err == nil {
		return TypeBIGINT
	}
	if _, err := strconv.ParseFloat(val, 64); err == nil {
		return TypeDOUBLE
	}
	lower := strings.ToLower(val)
	if lower == "true" || lower == "false" {
		return TypeBOOLEAN
	}
	return TypeVARCHAR
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
