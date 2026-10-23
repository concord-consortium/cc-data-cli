# Implementation Plan: cc-data: fetch CLUE documents, history and content into a dataset

**Jira**: https://concord-consortium.atlassian.net/browse/REPORT-110
**Requirements Spec**: [requirements.md](requirements.md)
**Status**: **In Development**

## Implementation Plan

The steps run in order and each leaves `go test ./...` green. The first three change no behavior (they reshape the store engine under the existing tests, which are what guard them); the fourth adds the CLUE store types and views; the rest add the fetch, the commands, the hint and the prose.

### Store type registry and per-type identity

**Summary**: One declaration per store type replaces the `answers|history` branches and regexes, and `Identity` becomes the ordered values of the type's identity columns. Only `answers` and `history` are registered here, with exactly today's identity, pins and file names, so no file, column map or query changes (R8, R9).

**Files affected**:
- `internal/store/kind.go` (new): `Kind`, the registry, key encode/decode.
- `internal/store/identity.go`: `Identity` as values; token-scan extraction.
- `internal/store/membership.go`: per-kind line encoding.
- `internal/store/columns.go`: `NewColumnDetector(kind)`; pins move onto the kind.
- `internal/store/{merge,segment,mergesource}.go`: take a `Kind` where they took `typ string`; `BuildIndex` checks each segment line with `json.Valid` before the identity scan, since the scan stops early and would otherwise accept a torn line.
- `internal/dataset/{merge,reindex,summary}.go`: `storeVersionRe`, `reindexStoreRe`, `reindexMemberRe`, `finalArtifactRe`, `manifestTotals` from the registry.
- `internal/dataset/dataset.go`: `artifactFileRe`, the match `dataset purge` and `dataset delete` remove files by, from the registry.
- `internal/duck/{views,materialize}.go`: `statements()` loops the registry for store views; `perStoreView` takes `USING` from the kind; `isStoreType` and `IdentityColumnNames` from the registry; `membershipColumns` derived from the kinds. `contractStoreColumns` stays as it is until the CLUE step deletes it.
- `internal/dataset/attachments.go`, `internal/fetch/attachments.go`: read `source_key`, `remote_endpoint`, `question_id` and `history_id` through `Kind.Field(id, name)` instead of struct fields; attachment scanning stays on `answers` and `history`.
- `internal/store/identity_test.go`, `internal/dataset/crash_test.go`: construct identities through the kind.
- `internal/dataset/testdata/v0.2/` (new): a dataset written and materialized by `main` before this story's first step (answers and history stores, memberships, the `materialized/` Parquet, manifest), generated once before this step and committed.

**Estimated diff size**: ~450 lines

```go
// Kind declares one store type. Every place that handles "each store type" reads this
// registry rather than spelling the type names.
type Kind struct {
	Name     string            // store type, file stem and view name
	Identity []string          // identity columns, in key order
	Optional map[string]bool   // identity columns written to membership only when non-empty
	Strict   bool              // an absent, null, non-string or empty identity value is an error
	Pinned   map[string]string // DuckDB types fixed regardless of observed values
	Route    string            // path under /api/v1/reports/<run>/
	DoubleDecode bool          // double-decode report_state (LARA answers and history)
}

var kinds = []Kind{answersKind, historyKind} // the CLUE kinds join in a later step

func Kinds() []Kind                    { ... } // registry order
func KindOf(name string) (Kind, bool)  { ... }
func Names() []string                  { ... } // for the derived regexes

type Identity []string // values aligned with Kind.Identity

func (k Kind) Key(id Identity) string   { return encodeFields(id...) }
func (k Kind) Decode(key string) Identity { ... } // inverse of encodeFields; panics only on a key this package did not encode
func (k Kind) Field(id Identity, col string) string { ... } // "" when col is not an identity column
```

`answersKind` is `Identity: source_key, remote_endpoint, question_id`; `historyKind` adds `history_id` with `Optional: {history_id}`; both carry today's whole `pinnedColumns` map and `DoubleDecode: true`, so their column maps are byte-identical to 0.2.x's.

**Identity extraction.** `IdentityFromRecord(k Kind, rec []byte) (Identity, error)` scans tokens with `json.Decoder` and stops once every identity column has been seen (measured 1.6 µs on a 2.3 KB CLUE record against 7.7 µs for a struct unmarshal and 9.3 µs for a map, so about 10 s rather than 50 s per pass over 6.38M records). It matches top-level keys only and skips every nested value whole, as the struct unmarshal it replaces does: in a stored line a nested object can precede the identity and hold a key of the same name (`history`'s `report_state` carries the interactive's own state object and sorts before `source_key`; `clue_documents`' `firestore_metadata` holds `uid` and `context_id` and sorts before `uid`). Non-strict kinds keep today's behavior: absent or null is `""`, a non-string is an error. Strict kinds error on all four (R5).

**Membership lines** are written by hand in identity-column order, each value through `json.Marshal(string)` (HTML-escaping, like `json.Encoder`), skipping an `Optional` column whose value is empty. For `answers` and `history` that reproduces `json.Encoder`'s output byte for byte; a golden test compares the two over hostile strings (`<&>`, quotes, `\x1f`, non-ASCII, empty).

**Derived regexes.** `regexp.MustCompile("^(" + alternation(store.Names()) + `)\.v(\d+)\.jsonl$`)` and the membership form, with each name passed through `regexp.QuoteMeta`. Anchoring keeps `history` from matching inside `clue_history`.

**Tests**: the existing suite unchanged in behavior; `IdentityFromRecord` on stored lines whose nested objects carry decoy identity keys ahead of the real ones (a `history` record with `source_key` and `question_id` inside `report_state.interactiveState`, a CLUE-shaped record with `uid` inside `firestore_metadata`) returns the top-level values; `TestIdentityEncodingInjective` and `TestIdentityHistoryIncludesHistoryID` rewritten against `Kind.Key`; the membership golden test; a `Decode(Key(id)) == id` round trip over hostile values; a test that every registered kind's store and membership file names round-trip through the derived regexes and match `isArtifactFile`; a test that copies `testdata/v0.2/` and opens, queries, re-merges and reindexes it, with its store and membership files byte-identical after a no-op re-merge (R9). A materialized copy's freshness compares each input's size and modification time (`dataset.Fingerprint`), and neither a git checkout nor the copy keeps the times the fixture's manifest recorded, so the copy helper sets each recorded input's time back from its `<size>-<mtime>` fingerprint with `os.Chtimes`. Without that, every materialized view in the copy reads stale whatever the code does.

---

### Compact segment index and a merge iterator

**Summary**: The segment index stops holding a map of full identities. It becomes a slice of `(key, offset, length)` sorted by key with duplicates resolved last-wins by offset, and `MergeSource` becomes an iterator so a multi-run sweep is a k-way merge of those slices rather than a map over every key (R11).

**Files affected**:
- `internal/store/segment.go`: `SegmentIndex` as a sorted slice; `BuildIndex` keeps the registry step's `json.Valid` check, reads the key with `IdentityFromRecord` and keeps no `Identity`.
- `internal/store/mergesource.go`: `MergeSource` iterator; `SingleSource`, `MultiSource` rewritten.
- `internal/store/merge.go`: `StreamMerge` consumes the iterator.
- `internal/store/segment_fault_test.go`: unchanged assertions on the new type.

**Estimated diff size**: ~300 lines

```go
type indexEntry struct {
	key    string
	offset int64
	length int32
}

// SegmentIndex is the segment's identities in key order, one entry per key (the last
// occurrence in the file wins, as before). Records stay on disk and are read by offset.
type SegmentIndex struct {
	f        *os.File
	kind     Kind
	entries  []indexEntry
	rawCount int
}

// MergeSource yields one winning record per key, in key order, with the run that owns it.
type MergeSource interface {
	Next() (key string, rec []byte, run int, ok bool, err error)
	RawCount(run int) int
	Runs() []int
}
```

A segment line that is not valid JSON fails `BuildIndex`, as the struct unmarshal does today: the token scan stops at the identity and would otherwise pass a torn line, which a crash mid-append leaves and the resumed fetch's first record is glued onto. `BuildIndex` appends entries, `sort.SliceStable` by key, then keeps the last entry of each run of equal keys (stable sort keeps file order inside a run, so last means latest). `MultiSource` holds one cursor per run's slice; `Next` takes the smallest head key and, where several runs share it, reads each candidate's `(_fetched_at, _run_id)` stamp and keeps the newest, as `readStamp` does today, but only for keys that collide rather than for every key. `StreamMerge` keeps its old-store loop and counting; it pulls from `Next` instead of indexing `Keys()`. `SegmentIndex.Identities()` stays for this step, now decoding its keys, because `WriteMembership` and `membershipUnionMulti` still take identity slices until the next step.

**Tests**: the dataset merge, sweep, crash, fault-injection and concurrency suites unchanged; a segment whose page starts with a record cut inside `entry_json` and followed by that record again fails the merge and leaves the store as it was; a `BuildIndex` test with an identity repeated three times in one segment that keeps the last; a `MultiSource` test with three runs sharing keys whose stamps tie on `_fetched_at` and break on `_run_id`.

---

### Streamed membership and the covered walk

**Summary**: Membership is written and read as a stream in key order, and `covered` stops being a map: the merge asks a walker, in the old store's key order, whether any run other than those being merged holds each old key. The merging runs' own identities need no place in it: they are exactly the source's keys, so an old key equal to one takes `StreamMerge`'s replace branch and never reaches the covered check. Memory no longer grows with the old store or with other runs' membership (R11). The 1M-identity benchmark lands here.

**Files affected**:
- `internal/fsutil/fsutil.go`: `WriteAtomic0600(path, func(io.Writer) error)`, with `WriteFileAtomic0600` rebased onto it so the temp, fsync and rename sequence lives once.
- `internal/store/membership.go`: `(*SegmentIndex).WriteMembership(path)` (buffered, line by line, through `fsutil.WriteAtomic0600`) and `MembershipReader` (a scanner yielding keys, checking each line's key is above the previous one).
- `internal/store/covered.go` (new): the walker.
- `internal/store/merge.go`: `StreamMerge(oldStorePath string, k Kind, tmpPath string, src MergeSource, covered *Covered)`.
- `internal/dataset/merge.go`: `membershipUnionMulti` becomes `openCovered`, opening one reader per non-merging run's membership file of the kind; `WriteMembership` streams `SegmentIndex` keys through `Kind.Decode`.
- `internal/fetch/paged.go`: `computeCoverage` reads membership through the reader. `ReadMembershipFile` stays for `dataset/attachments.go`, which loads one run's answers or history keys into a map to filter attachment references.
- `internal/store/merge_bench_test.go` (new).

**Estimated diff size**: ~350 lines

```go
// Covered answers, for keys asked in non-decreasing order, whether any source holds the
// key. Each source is a key-ordered stream; a source found out of order is an error,
// which fails the merge before its store rename (fail closed, never drop a record).
type Covered struct {
	srcs  []keySource
	lastQ string
}

func (c *Covered) Has(key string) (bool, error)

// Drain reads every source to its end, checking order. StreamMerge calls it after the old
// store is exhausted: a source whose disorder sits past the last old key would otherwise
// never be read, and its out-of-order key could have been a record the walk dropped.
func (c *Covered) Drain() error
```

A throwaway model of `Has` agreed with the map semantics over 2,000 random trials, and caught a disorder only once the walk advanced past it, which is why `Drain` exists. Every membership file on disk is key-ordered already: the one writer, `WriteMembership`, has always been handed `SegmentIndex.Identities()`, which is in key order, since REPORT-77 introduced the merge engine.

**The memory test.** `TestMergeMemoryPerIdentity` (skipped under `-short`) writes a 1M-record `clue_history`-shaped segment (two 20-character id fields), builds the index, and asserts the heap delta after `runtime.GC()` is at most 120 bytes per identity. The prototype of this index measured 80; today's measured 570, so the test fails against the old engine. Until the CLUE kinds exist, it uses the `history` kind with the two ids in `source_key` and `question_id`: that key is 4 bytes longer (two empty fields) and lands in Go's 64-byte allocation class, so it measures about 99 bytes per identity rather than 83 (both measured on 1M keys); the CLUE step moves it to the `clue_history` kind. A second part covers what the index alone cannot: run 1 lands 1M history-shaped records, then run 2 merges a 1-record segment, so the walk streams the 1M-record store against run 1's 1M-line membership; peak heap in use, sampled every 5 ms, stays under 32 MB. Today's engine peaked at 363 MB on that merge (the `covered` map and the membership read whole), and a throwaway streamed walk over the same files at 4 MB, so a `Covered` or `openCovered` that loads its sources fails it.

**Tests**: the existing suites; an old store with keys held only by another run's membership is kept, one held by nobody is removed; a membership file written out of order fails the merge and leaves the manifest, store and segment as they were; the same with the disorder after the last old key (`Drain`); a membership file streamed by the new writer is byte-identical to the old encoder's for the same identities.

---

### CLUE store types, views and catalog rows

**Summary**: Registers `clue_documents`, `clue_history` and `clue_content` with their identity and pins, which by the first step's derivation gives them store files, membership, reindex, totals, orphan checks, materialization and per-run views. Adds the three static views, the widened `run_membership`, and in the same commit the catalog rows REPORT-104's guard demands for new views and identity columns (R7, R8, R10, R13, R14, R15, R17's view and identity rows).

**Files affected**:
- `internal/store/kind.go`: three `Kind`s, `Strict: true`, `DoubleDecode: false`, `Route` `clue/documents`, `clue/history`, `clue/content`. Each kind pins only its own route's fields, at R10's types, plus `_fetched_at` and `_run_id`:
  - `clue_documents` (R13's 19): `doc_id`, `uid`, `context_id`, `portal_class_id`, `owner_kind`, `type`, `visibility`, `title`, `unit`, `investigation`, `problem`, `group_id`, `network`, `offering_id`, `tools`, `created_at`, `metadata_sources`, `firestore_metadata`, `rtdb_metadata`.
  - `clue_history` (R14's 15): `doc_id`, `entry_id`, `idx`, `prev_entry_id`, `server_created`, `created`, `model`, `action_raw`, `undoable`, `state`, `n_records`, `is_revert`, `entry_uid`, `parse_ok`, `entry_json`.
  - `clue_content` (R15's 10): `doc_id`, `uid`, `context_id`, `found`, `type`, `version`, `change_count`, `last_history_entry_id`, `self`, `content_json`.

  One map shared by the three would not fail any query, which is why it needs saying: `ColumnDetector.Map` returns every pinned column whether or not a record carried it, so every CLUE store and view would gain the other two routes' columns as always-NULL, and REPORT-111's `clue_history`, which selects `h.*` beside the documents columns it joins (`unit`, `offering_id`, ...), would get both, the joined ones renamed `<name>_1`.
- `internal/duck/views.go`: `storeView(k)` falls back to `typedEmpty(k.Pinned)` for every kind, so `contractStoreColumns` and the `IdentityColumnNames` comment that names it are deleted, and the `answers` and `history` stand-ins gain the five pins they lacked (`_decode_error`, `report_state`, `report_state_raw`, `interactive_state_raw`, `answer`), every existing column keeping its type; `runMembershipView` selects every registered identity column on every member, casting the ones a type lacks to `NULL::VARCHAR`; the fallback lists every identity column.
- `cmd/dataset_show.go`: the totals column widens from `%-12s` to fit `clue_documents`.
- `internal/guidance/src/core.md`: Views entries, the `run_membership` entry's CLUE `USING` lists and the `doc_id`-only fan-out, the per-run views line, the Identity columns section made per store with `doc_id` and `entry_id`, the cohort join with R17's per-assignment caveat.
- `docs/researcher-guide.md`: three rows in section 5's view table.
- `internal/duck/views_test.go`: `TestIdentityColumnNamesAreTheMembershipKey`'s list gains `doc_id` and `entry_id`.
- `internal/store/merge_bench_test.go`: the memory test's index part moves to the `clue_history` kind.
- `internal/duck/clue_views_test.go` (new).

**Estimated diff size**: ~350 lines

The Identity columns section stays one top-level bullet per column, led by the column name, with the stores it keys named in its text, as the existing four entries are. `ParseCatalog` takes the leading backticked names of an unindented bullet or table row, so a bullet or row led by a store name documents that store as an identity column, and an indented bullet documents nothing; either fails `TestGuidanceDocumentsEveryIdentityColumn`. `core.md` must not name a command (`TestCoreNamesNoCommand`), so the CLUE-versus-LARA distinction there is between the views `history` and `clue_history`; the command names go in `skill_header.md` with the commands.

**Tests**: on a dataset with no CLUE data, each CLUE view binds with exactly its own kind's pinned columns at their types (its `DESCRIBE` equals the list above plus the two stamps, so a shared map fails it) and returns no rows, and `SELECT doc_id FROM run_membership` binds; on a dataset with one store of each type, `run_membership` holds both kinds of row with the other kind's columns NULL, `clue_history_<run>` returns exactly the run's entries, `answers_<run>` is unchanged; pinned types hold when the fixture's values would sniff otherwise (an all-integer `created_at` and `change_count`, which would sniff as BIGINT, stay JSON, and an all-null `offering_id` stays BIGINT); `dataset reindex` adopts the three stores and memberships and the show totals carry them; on a copy of `testdata/v0.2/` made by the first step's helper (input times restored), `dataset show` reports `STALE_MATERIALIZED` for `run_membership` and for no other view (R9); materializing a CLUE view writes its Parquet with the same rows; the guidance guard.

---

### CLUE fetch: routes, identity check and coverage by owner

**Summary**: `FetchPaged` takes its route and its decoding from the kind, refuses an item without its identity before the page is written, and computes CLUE coverage by owner (R4, R5, R6, R12).

**Files affected**:
- `internal/fetch/paged.go`: route `"/api/v1/reports/%d/" + kind.Route`; `doubleDecode` only when `kind.DoubleDecode`; the identity check; `history_mode` only for `history`; `computeCoverage` dispatches by kind.
- `internal/fetch/clue_coverage.go` (new): the two walks.
- `internal/fetch/clue_test.go` (new), `internal/fetch/testdata/clue/*.json` (new): fixtures.

**Estimated diff size**: ~400 lines

**The identity check.** For a `Strict` kind, every item of a page goes through `store.IdentityFromRecord` before `AppendPage`; the first failure returns `&output.CLIError{ExitCode: output.ExitContract, Code: "MALFORMED_ITEM", Message: "run 584 clue_history: item 17 of page 12 has no entry_id"}`. Nothing of that page is appended and the cursor stays on the previous page.

**Coverage.**
- `clue_documents`, `clue_content`: stream the run's membership keys against the kind's own store (both key-ordered); for each store record whose key is a member, read the top-level `context_id` and `uid` with the token scan (never `firestore_metadata`'s, which precede `uid` in a stored line) and add the pair to an owner set (at most `total_endpoints` entries).
- `clue_history`: walk the membership keys, decoding each key's `doc_id` and skipping repeats (one document's entries are contiguous, checked in the requirements' verification), against the `clue_documents` store; a document absent from it makes the coverage `nil`, and the result carries `"coverage_note": "owners not counted: fetch clue-documents for run <id> before clue-history"`. A later documents pull does not revisit it: the history's `total_endpoints` went with its cursor, and the manifest keeps it only inside the coverage.
- `queried` from the persisted `total_endpoints`, `empty = queried - with_data`. A read error leaves coverage `nil` rather than counting zero owners, which is what today's computation reports when its membership read fails.
- Both walks run under the per-dataset lock (`DS.Lock()`, taken after `MergeCompact` returns, since that lock is not reentrant), reading the manifest and opening both files inside it. Outside it, another run's pull of the same kind can land between the manifest read and the open, and its `cleanupOldVersions` deletes the store version the manifest named, so the walk fails and the coverage is lost. Today's computation is safe without the lock only because it reads nothing but its own run's membership file, which another run's merge never removes. A merge already holds this lock for its whole store rewrite, so the walk, seconds at Scott's scale, adds no new kind of wait.

**Fixtures.** One JSON page body per route, written to match REPORT-109's R13 to R15 field for field and CLUE-742's builders (`toDocumentRow`, `toHistoryRow`, `toContentRow` in `functions-v2/src/clue-export-walk.ts`): student, group and class owners; a document with `firestore_metadata` only; a `found: false` content row; history entries with a duplicate `idx`, one with `idx` and `created` both null (the export's nulls for an unsafe index and an out-of-range time), `parse_ok: false`, and an `entry_json` holding `<`, `&`, non-ASCII and a 3 MB string. The fake server is `paged_test.go`'s `bulkServer`, extended to record request paths.

**Tests**: each route requested at its own path and nowhere else; stored `entry_json`/`content_json` equal the fixture strings, compared through the view with `md5`; a history pull interrupted after page 2 of 4 (the cursor left mid-document) and resumed equals an uninterrupted pull, with a page re-delivered on resume and no duplicate; an `EXPIRED_CURSOR` restart; a re-pull with one entry changed (updated in place) and one document out of scope (removed); two runs sharing a document, where re-pulling run B without it keeps it in `clue_documents` (run A still holds it) and drops it from `clue_documents_<B>`; an item missing `entry_id`, with `doc_id` null, and with a numeric `doc_id`, each refused with `MALFORMED_ITEM` and nothing appended; coverage for each kind, history with and without the documents fetched first; a test hook inside the coverage walk finds the dataset lock held (`TryLock` from another goroutine fails), which fails if the walk runs outside it; answers and history coverage unchanged.

---

### `get clue-*` commands and MCP tools

**Summary**: The three verbs and three tools over `FetchPaged`, the help cross-references, and the `tools.md` rows the guard requires for the new tools (R1, R2, R3, R17's tool rows).

**Files affected**:
- `cmd/get_clue.go` (new): three constructors over `newPagedGetCmd` with `Long` help.
- `cmd/get.go`: registers them; `Short` mentions CLUE.
- `cmd/get_history.go`: `Long` help naming `get clue-history`.
- `internal/mcpserver/tools.go`: three `addTool` calls with `pagedHandler(kind)`.
- `internal/guidance/src/tools.md`: one entry for the three tools.
- `internal/mcpserver/server_test.go`: `TestMCPToolSurface`'s list and the description table.
- `cmd/get_clue_test.go` (new).

**Estimated diff size**: ~200 lines

`newPagedGetCmd(use, typ, short string)` gains a `long string`; the existing two pass `get history`'s cross-reference and `""`.

**Tests**: `cc-data get clue-history --help` names `get history` and says CLUE document history, and `get history --help` names `get clue-history`; a command run against the fake server emits a result line with `type: "clue_history"` and the counts; the MCP surface lists exactly the tools plus the three, with the Student ID Mapping wording, and `get_clue_history`'s description names `get_clue_documents` as the fetch to run first; `get_clue_history`'s result equals the CLI's.

---

### Large-store hint in `dataset show`

**Summary**: `LARGE_UNMATERIALIZED` when a store file is over 4 GiB and its view has no fresh Parquet (R16).

**Files affected**:
- `internal/duck/materialize.go`: `ShowJSON` adds the warning beside the stale-copy warnings it already decides, using `staleMaterializedViews`' freshness and a stat of each registered store's file.
- `internal/duck/materialize_test.go`.

**Estimated diff size**: ~80 lines

The threshold is a package variable so the test can lower it rather than write 4 GiB.

**Tests**: a store over the (lowered) threshold with no Parquet warns, naming the view, its size and `cc-data dataset materialize <ref>`; with a fresh Parquet it does not; with a stale one it does; a store under the threshold never does.

---

### Prose the change invalidates

**Summary**: The README, researcher guide and skill header describe the new commands and the per-type identity (R18).

**Files affected**:
- `README.md`: the `get` command list, the dataset layout (CLUE store and membership files), and the duplicate-free paragraph's list of identities.
- `docs/researcher-guide.md`: command lists in sections 3 and 4.
- `internal/guidance/src/skill_header.md`: the three commands under "Fetching data", `clue-documents` first, `clue-history` described against `history` and saying its owner coverage needs the run's documents fetched before it (as `attachments` says what it needs fetched first).
- Comment headers in the store and fetch packages that state the identity contract (`identity.go`, `paged.go`'s `PagedOptions`), rewritten to the per-kind contract.

**Estimated diff size**: ~120 lines

The researcher guide's `clue_prepull` line (section 7, "cc-data cannot fetch CLUE data yet") is left for REPORT-152, which removes the setting and its refusal in the same release.

## Rollout and acceptance

Nothing deploys with this story; it ships in cc-data-cli 0.3.0 with REPORT-111 and REPORT-152. R20's staging acceptance runs once REPORT-109 (report-service #445) and CLUE-742 (collaborative-learning #3052) are deployed to staging, against a run covering class `7be899cf…`, comparing counts with REPORT-109's own staging check.

## Open Questions

### RESOLVED: Judgment call: change the merge engine under the existing tests before adding any CLUE type
**Options considered**:
- A) Registry, then index, then covered walk, each behavior-neutral, before the CLUE kinds.
- B) Add the CLUE kinds first and rework the engine last.

**Decision**: A. The existing dataset suites (merge, sweep, crash, fault injection, concurrency) are the strongest test of a merge rework, and they only see answers and history; reworking first means every later step runs on the engine they proved.

### RESOLVED: Judgment call: `MultiSource` reads stamps only on colliding keys
**Options considered**:
- A) k-way merge; read `(_fetched_at, _run_id)` only where runs share a key.
- B) Keep reading every key's stamp, as today.

**Decision**: A. Today's `NewMultiSource` reads every record of every swept segment to decide a winner that only matters on collisions; at Scott's scale that is a second 15 GB read.

### RESOLVED: Low confidence: whether the token scan finds CLUE identities early
**Context**: The 1.6 µs figure stops once `doc_id` and `entry_id` are read. `AppendPage` re-marshals each record from a map, which writes keys in sorted order, so `doc_id` and `entry_id` precede `entry_json` in every stored record; on a record where they came last the scan would cost about what a struct unmarshal does.
**Decision**: Keep the token scan. Stored records always have the sorted order, which puts the identity early in `history`, `answers`, `clue_history` and `clue_documents`. `clue_content` is the exception: `content_json` sorts before `doc_id` and `context_id`, so the scan decodes the whole content string first, which costs more than the struct (fourth pass). That store holds one row per document, so the cost stays small.

## Self-Review

Roles: commit reviewer, test writer, operator, senior engineer. Claims about code that does not exist yet were checked by building the piece in question as throwaway code (deleted).

### Commit reviewer

#### RESOLVED: the registry step missed two packages that read identity fields
`internal/dataset/attachments.go:94,285-288` and `internal/fetch/attachments.go:245` read `Identity.SourceKey` and its siblings, so the first step would not compile with `Identity` as values. They are now in its file list, through `Kind.Field`.

#### RESOLVED: the index step removed `Identities()` while its callers still needed it
`dataset/merge.go:106-109` hands `Identities()` to `WriteMembership` and `membershipUnionMulti` until the covered-walk step replaces both. The index step keeps it, decoding keys.

#### RESOLVED: `computeCoverage` is not the only other reader of membership files
`dataset/attachments.go:175` also calls `ReadMembershipFile`. It stays, for answers and history only.

### Test writer

#### RESOLVED: R9's 0.2.x dataset had no fixture behind it
"The existing suite" builds its datasets with the code under change, so it cannot show that files written by 0.2.x still read. The first step commits a dataset generated by `main` before this story's first step and tests it, byte-identical after a no-op re-merge. Hand-written membership lines were checked byte-identical to `json.Encoder`'s over 64 hostile identities, including invalid UTF-8 and U+2028.

#### RESOLVED: the shared-document test was not stated as something that can fail
Rewritten so the assertion names which view keeps the document and which drops it.

### Operator

#### RESOLVED: no streaming atomic write existed for membership
`fsutil.WriteFileAtomic0600` takes a byte slice, which is the whole-file buffer R11 removes. The covered-walk step adds `WriteAtomic0600` with a writer callback and rebases the old function on it.

#### RESOLVED: `dataset show` totals misalign on `clue_documents`
`cmd/dataset_show.go` prints totals as `%-12s`; three of the new names are 12 to 14 characters. Widened in the CLUE-types step.


### Second pass

Roles: senior engineer, test writer, commit reviewer. Each finding was checked against `main` or throwaway code (deleted).

#### RESOLVED: the token scan would let a torn segment line into the store
`AppendPage` appends with `O_APPEND` and a resume never truncates, so a crash inside a page's first record leaves that record's head with the resumed fetch's copy of it glued on. Today's `json.Unmarshal` refuses the line and the merge fails closed. A throwaway token scan, the shape the registry step plans, returned the line's `doc_id` and `entry_id` with no error, so the line would win as the last occurrence of its key and land in the store, where DuckDB 1.5.5's `read_json` then fails every query ("Malformed JSON ... unexpected character", checked with the CLI). `BuildIndex` now checks each line with `json.Valid` (6 µs on a 2.4 KB history record, against 8 µs for the struct unmarshal it replaces), so steps 1 to 3 keep today's behavior; old-store lines keep the fast scan, since only validated segment lines reach a store.

#### RESOLVED: the memory test measured the index and nothing else
R11 also says the merge does not grow with the old store or other runs' membership, and no test could fail on that: a `Covered` built over maps would pass. A throwaway test on `main` measured 363 MB peak heap in use for a 1-record merge over a 1M-record store held by another run's membership, and a throwaway streamed walk over the same two files 4 MB. The covered-walk step's memory test now asserts that merge's peak under 32 MB.

#### RESOLVED: the `history` stand-in key is not the CLUE key's size
Measured on 1M keys: 82.7 bytes per identity for the `clue_history` key (46 bytes) and 98.7 for the `history` stand-in (50 bytes, Go's 64-byte class). Still under 120, but not "the same key size" as written. The text now says so, and the CLUE step moves the test to the `clue_history` kind.

#### RESOLVED: the CLUE step left two guards unplanned
`TestIdentityColumnNamesAreTheMembershipKey` (`internal/duck/views_test.go:53`) hard-codes the four identity columns and fails once `doc_id` and `entry_id` register; it is now in the step's files. `ParseCatalog`'s pattern, run against candidate lines, read ``- `answers`: `source_key`, ...`` and ``| `clue_history` | `doc_id`, `entry_id` |`` as documenting `answers` and `clue_history`, and an indented bullet as nothing, so a per-store layout fails the identity guard. The step now says how the section is laid out.

### Third pass

Roles: senior engineer, commit reviewer, test writer, and the downstream stories (REPORT-111's spec on `REPORT-111-clue-views`, REPORT-152's on `REPORT-152-recent-logs`). Each finding was checked against `main` or throwaway code (deleted). None changes an interface those two consume: `IdentityFromRecord(k, rec)`, the `Kind` fields, the registry loop with `storeView(k)` falling back to `k.Pinned`, `run_membership`'s columns and `openCovered` stay as they read them.

#### RESOLVED: the token scan did not say it reads top-level keys only
A throwaway test appended a `history` record through `Segment.AppendPage` whose `report_state.interactiveState` held its own `source_key`: the stored line put `report_state` (with its nested object kept in received order) before the top-level `source_key`, the struct unmarshal returned the real value, and a token scan that matched names at any depth returned the nested one. `fetch/decode.go` merges the interactive's state object into `report_state`, so the nested keys are whatever an interactive author chose. The same test on a `clue_documents` line put `firestore_metadata`, which holds `uid` and `context_id`, before the top-level `uid`, the field R12's coverage reads. A misread identity would land the record under another key with no error. The registry step now says the scan matches top-level keys only and tests it with decoys, and the coverage step says the same of the owner read.

#### RESOLVED: the CLUE step left `contractStoreColumns` half-used
The registry step derived `contractStoreColumns` from the kinds, and the CLUE step made `storeView(k)` fall back to `typedEmpty(k.Pinned)`, which left it unclear whether `answers` and `history` kept the old stand-in, and `contractStoreColumns` with no reader if not. One rule now: every kind's stand-in is its pins, and the CLUE step deletes `contractStoreColumns`. A throwaway run of `go test ./...` with the `answers` and `history` stand-ins widened to their pins passed, so no test or view depends on the narrower shape.

#### RESOLVED: no test covered the materialized copies R9 now describes
The CLUE step changes `run_membership`'s SQL and therefore its signature. The 0.2 fixture is now materialized when generated, and the CLUE step asserts `dataset show` flags `run_membership` alone, so a change that also altered the `answers` or `history` view's SQL (and made every researcher's copy of the largest views stale on upgrade) fails it.

### Fourth pass

Roles: senior engineer (concurrency), test writer, downstream consumer (REPORT-111 on `REPORT-111-clue-views`, REPORT-152 on `REPORT-152-recent-logs`), performance engineer. Each finding was checked against `main`, those specs, or throwaway code (deleted). None changes an interface REPORT-111 or REPORT-152 consume: the per-kind pins are the shape both already assume (REPORT-111's A3 and `cluePins`), and the lock is internal to the fetch.

#### RESOLVED: CLUE coverage read a store file another run's merge can delete
R12's walks read a store file (`clue_documents`' own, or `clue_documents` for history), and `FetchPaged` computes coverage after `MergeCompact` has released the per-dataset lock, while the activity lock lets another run's pull of the same kind merge meanwhile. A throwaway test on `main` merged run 1, read the manifest's store name, merged run 2 of the same type, then opened the name: `no such file or directory`, since `cleanupOldVersions` removes superseded store versions; run 1's own membership file was still there, which is why today's coverage, reading only that file, never needed the lock. The coverage step now walks under `DS.Lock()` with a test that fails without it, R12 says so, and the read-error sentence no longer claims today's computation leaves coverage nil (it reports zero owners).

#### RESOLVED: the R9 fixture test could not pass
`Materialized.Fresh` compares each input's `<size>-<mtime>` fingerprint (`internal/dataset/fingerprint.go`), and a committed fixture's files get new times at checkout and again when the test copies them. A throwaway test on `main` materialized `fullFixture`, copied the folder by content into another root, and found all six materialized views stale (`answers`, `logs`, `report_prompts`, `reports`, `run_membership`, `student_id_mapping`), so "stale for `run_membership` and no other view" fails before any change is made. Restoring each recorded input's time with `os.Chtimes` from its fingerprint made the same copy read fully fresh, in a different directory, since the signature neutralizes the dataset path. The first step's copy helper now does that, and the CLUE step's test uses it.

#### RESOLVED: the CLUE step did not say which pins belong to which kind
R10 lists the pinned fields by DuckDB type across all three routes, and the step said only "pins as R10 lists". `NewColumnDetector` seeds every pinned column and `ColumnDetector.Map` returns them whether or not any record carried one (`internal/store/columns.go`), so one shared map would give every CLUE store the other routes' fields as always-NULL columns. REPORT-111 assumes they are absent (its A3: no R10 column on content) and selects `h.*` beside the documents columns it joins onto `clue_history`, where DuckDB would rename the joined duplicates to `<name>_1`. The step now lists each kind's fields, which together are exactly R10's 39, checked against `toDocumentRow`, `toHistoryRow` and `toContentRow` on `CLUE-742-clue-export-function`, and its empty-dataset test compares each view's `DESCRIBE` with its own list.

#### RESOLVED: the token scan is not early on `clue_content`
The low-confidence entry said the scan's worst case is the struct unmarshal's and that sorted order always puts the identity first. `AppendPage` writes keys sorted, and `content_json` sorts before `context_id` and `doc_id`. A throwaway benchmark on a 5.2 MB `toContentRow`-shaped line (a 3 MB content string) found `doc_id` at byte 5,187,932 of 5,188,063; the token scan took 23.7 ms and allocated 22.9 MB, against 16.6 ms and 0.8 MB for the struct and 13.5 ms for `json.Valid`. The entry now names the exception; the decision stands, since that store has one row per document.
