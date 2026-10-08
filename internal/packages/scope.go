package packages

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

var classHashRe = regexp.MustCompile(`^[0-9a-f]{48}$`)

// Class and Assignment are scope.json's entries.
type Class struct {
	ClassHash string `json:"class_hash"`
	ClassID   int64  `json:"class_id"`
}

type Assignment struct {
	OfferingID int64   `json:"offering_id"`
	RunnableID int64   `json:"runnable_id"`
	Name       *string `json:"name"`
	URL        string  `json:"url"`
}

// LocalScope is the four keys rigse supplies on the VM, which an author writes for a laptop.
type LocalScope struct {
	Kind        string       `json:"kind"`
	ID          string       `json:"id"`
	Classes     []Class      `json:"classes"`
	Assignments []Assignment `json:"assignments"`
}

// ScopeFile is scope.json as the runner writes it: ClueSource is its constant "firebase", and
// Dataset the bare name, as it writes pkg-<id>, while RD_DATASET carries the full ref.
type ScopeFile struct {
	LocalScope
	ClueSource string `json:"clue_source"`
	Dataset    string `json:"dataset"`
	OutputDir  string `json:"output_dir"`
}

// MarshalJSON keeps the runner's key order, so a diff between a local and a VM scope.json is
// only the values.
func (s ScopeFile) MarshalJSON() ([]byte, error) {
	type ordered struct {
		Kind        string       `json:"kind"`
		ID          string       `json:"id"`
		Classes     []Class      `json:"classes"`
		Assignments []Assignment `json:"assignments"`
		ClueSource  string       `json:"clue_source"`
		Dataset     string       `json:"dataset"`
		OutputDir   string       `json:"output_dir"`
	}
	return json.Marshal(ordered{s.Kind, s.ID, s.Classes, s.Assignments, s.ClueSource, s.Dataset, s.OutputDir})
}

// ParseLocalScope reads an author's scope file, refusing any key the four do not name (the
// other three are the runner's to set) and any shape the runner would never send.
func ParseLocalScope(data []byte) (LocalScope, error) {
	var s LocalScope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, fmt.Errorf("scope file: %v (it holds exactly kind, id, classes and assignments)", err)
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return s, fmt.Errorf("scope file: it must hold one JSON object and nothing after it")
	}
	switch {
	case s.Kind == "":
		return s, fmt.Errorf("scope file: kind is required")
	case s.ID == "":
		return s, fmt.Errorf("scope file: id is required")
	case len(s.Classes) == 0:
		return s, fmt.Errorf("scope file: classes must name at least one class")
	case s.Assignments == nil:
		return s, fmt.Errorf("scope file: assignments is required (an empty list is allowed)")
	}
	for i, c := range s.Classes {
		if !classHashRe.MatchString(c.ClassHash) || c.ClassID < 1 {
			return s, fmt.Errorf("scope file: classes[%d] needs a 48-hex class_hash and a positive class_id", i)
		}
	}
	for i, a := range s.Assignments {
		if a.OfferingID < 1 || a.RunnableID < 1 {
			return s, fmt.Errorf("scope file: assignments[%d] needs a positive offering_id and runnable_id", i)
		}
	}
	return s, nil
}

// AssignmentURLs is the scope's half of the URLs a package's patterns are matched against.
func (s LocalScope) AssignmentURLs() []string {
	var urls []string
	for _, a := range s.Assignments {
		if a.URL != "" {
			urls = append(urls, a.URL)
		}
	}
	return urls
}
