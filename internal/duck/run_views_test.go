package duck

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/concord-consortium/cc-data-cli/internal/store"
)

// The documented learner count, run-scoped on both sides, as the guidance gives it.
const learnersOfRun = "SELECT count(DISTINCT m.user_id) FROM student_id_mapping m JOIN run_answers a ON a.remote_endpoint = m.run_remote_endpoint WHERE a.run_id = %d AND m.run_id = %d"

// Each wrong way to count gives a different number here: counting endpoints instead of users
// (user 1 answered in two assignments), joining the bare-endpoint learner (who would borrow
// another bare answer), counting an answer once instead of once per run (e3/q1 is in runs 700
// and 800), and joining membership untyped (run 700's history holds e1a/q1 too).
func TestRunAnswersAndTheDocumentedLearnerCount(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 700, [][]byte{
		answerRec("s", "https://p/d/e1a", "q1", "a"), answerRec("s", "https://p/d/e1b", "q1", "b"),
		answerRec("s", "https://p/d/e3", "q1", "c"), answerRec("s", "https://p/d/", "q1", "bare"),
	})
	hist, _ := json.Marshal(map[string]any{"source_key": "s", "remote_endpoint": "https://p/d/e1a", "question_id": "q1", "history_id": "h1"})
	seg := store.OpenSegment(d.Dir, store.TypeHistory, 700)
	if err := seg.AppendPage([][]byte{hist}, time.Unix(700, 0).UTC(), 700); err != nil {
		t.Fatal(err)
	}
	seg.WriteCursor(&store.Cursor{Items: 1})
	if _, err := d.MergeCompact(store.TypeHistory, 700, seg); err != nil {
		t.Fatal(err)
	}
	buildStore(t, d, 800, [][]byte{answerRec("s", "https://p/d/e3", "q1", "c"), answerRec("s", "https://p/d/e9", "q1", "z")})
	row := func(learner, user, class, offering int, endpoint string) string {
		return fmt.Sprintf("%d,%d,%d,s%d,%d,%d,https://act/1,%s", learner, user, user, user, class, offering, endpoint)
	}
	addDimensionCSV(t, d, dimFixture{run: 700, slug: "student-id-mapping", fetchedAt: at(1), filter: `{}`, csv: mappingCSV(
		row(101, 1, 1, 70, "https://p/d/e1a"), row(102, 1, 1, 71, "https://p/d/e1b"),
		row(103, 3, 1, 70, "https://p/d/e3"), row(104, 4, 1, 70, "https://p/d/"))})
	addDimensionCSV(t, d, dimFixture{run: 800, slug: "student-id-mapping", fetchedAt: at(2), filter: `{}`, csv: mappingCSV(
		row(109, 9, 2, 90, "https://p/d/e9"))})
	e := openEngine(t, []DatasetSpec{{DS: d}}, nil)

	if n := queryInt(t, e, "SELECT count(*) FROM run_answers WHERE run_id = 700"); n != 4 {
		t.Errorf("run 700 answers = %d, want 4", n)
	}
	if n := queryInt(t, e, "SELECT count(*) FROM run_answers WHERE run_id = 800"); n != 2 {
		t.Errorf("run 800 answers = %d, want 2", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learnersOfRun, 700, 700)); n != 2 {
		t.Errorf("run 700 learners = %d, want 2 (users 1 and 3; user 4's endpoint is bare)", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learnersOfRun, 800, 800)); n != 1 {
		t.Errorf("run 800 learners = %d, want 1", n)
	}
}

func TestRunAnswersIsEmptyNotMissingOnAFreshDataset(t *testing.T) {
	e := openEngine(t, []DatasetSpec{{DS: newDS(t, "ds")}}, nil)
	if n := queryInt(t, e, "SELECT count(*) FROM run_answers"); n != 0 {
		t.Errorf("run_answers has %d rows", n)
	}
	if n := queryInt(t, e, fmt.Sprintf(learnersOfRun, 1, 1)); n != 0 {
		t.Errorf("the learner count binds to %d on a fresh dataset, want 0", n)
	}
}

func TestRunAnswersRunIDIsBigint(t *testing.T) {
	d := newDS(t, "ds")
	buildStore(t, d, 700, [][]byte{answerRec("s", "https://p/d/e1", "q1", "a")})
	for name, e := range map[string]*Engine{
		"with membership": openEngine(t, []DatasetSpec{{DS: d}}, nil),
		"fresh":           openEngine(t, []DatasetSpec{{DS: newDS(t, "fresh")}}, nil),
	} {
		rows, err := e.Query(context.Background(), "SELECT data_type FROM information_schema.columns WHERE table_name = 'run_answers' AND column_name = 'run_id'")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var typ string
		if rows.Next() {
			rows.Scan(&typ)
		}
		rows.Close()
		if typ != "BIGINT" {
			t.Errorf("%s: run_id is %s, want BIGINT", name, typ)
		}
	}
}
