package usage

import (
	"bufio"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	dateLayout = "2006-01-02"

	// unknownProvider / unknownModel are placeholders for unknown attribution or model.
	unknownProvider = "unknown"
	unknownModel    = "unknown"

	// slotLen is the resolution of the usage timeline.
	slotLen = 5 * time.Minute

	// headLen is how much of a file's start is fingerprinted, to tell an
	// appended file from a rewritten one of equal or larger size.
	headLen = 1024
)

// bucket is the cached, pre-aggregated usage of one file. Session and
// RawProvider are kept so attribution can be redone on every load.
type bucket struct {
	Date        string
	Engine      string
	Model       string
	RawProvider string
	Session     string
	Tokens      Tokens
	Requests    int
	Last        time.Time // latest request, used to pick the session record in effect
	// Slots is the day's timeline: total tokens per slotLen slot, keyed by
	// the slot's index from local midnight.
	Slots map[uint16]int64
}

type bucketKey struct{ Date, Model, RawProvider, Session string }

func (b *bucket) key() bucketKey { return bucketKey{b.Date, b.Model, b.RawProvider, b.Session} }

// fileState is one file's cached parse result plus what is needed to resume
// parsing from Offset when the file grows.
type fileState struct {
	Engine  string
	Size    int64
	Mtime   time.Time
	Offset  int64 // end of the last complete line parsed
	Head    uint64
	Buckets []bucket
	// Keys are the dedupe keys this file owns.
	Keys []uint64
	// State is the source parser's own resume state.
	State []byte

	// The last counted record, which a record with the same key supersedes.
	LastKey    uint64
	LastTokens Tokens
	LastBucket bucketKey
	LastSlot   uint16
}

type logFile struct {
	path  string
	src   Source
	size  int64
	mtime time.Time
}

// scanner brings the cache up to date with the logs.
type scanner struct {
	cache *Cache
	// owners maps each dedupe key to the file that counted it.
	owners map[uint64]string
}

// scan updates cache from the files under every source's roots and returns
// all buckets.
func scan(files []logFile, cache *Cache) []bucket {
	s := &scanner{cache: cache, owners: map[uint64]string{}}
	s.prune(files)
	for p, st := range cache.Files {
		for _, k := range st.Keys {
			s.owners[k] = p
		}
	}

	var out []bucket
	for _, f := range files {
		if st := s.update(f); st != nil {
			out = append(out, st.Buckets...)
		}
	}
	return out
}

// listFiles walks every source's roots. Files come oldest first, so the
// original session owns a request rather than a later fork that copied it.
func listFiles(roots map[Source][]string) []logFile {
	var files []logFile
	seen := map[string]bool{}
	for _, src := range sources {
		for _, dir := range roots[src] {
			if seen[dir] {
				continue
			}
			seen[dir] = true
			_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
					return nil
				}
				if fi, err := d.Info(); err == nil {
					files = append(files, logFile{path, src, fi.Size(), fi.ModTime()})
				}
				return nil
			})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if !files[i].mtime.Equal(files[j].mtime) {
			return files[i].mtime.Before(files[j].mtime)
		}
		return files[i].path < files[j].path
	})
	return files
}

// prune drops cache entries for files that no longer exist.
func (s *scanner) prune(files []logFile) {
	present := make(map[string]bool, len(files))
	for _, f := range files {
		present[f.path] = true
	}
	for p := range s.cache.Files {
		if !present[p] {
			delete(s.cache.Files, p)
		}
	}
}

// update returns the file's current state, parsing only what changed.
func (s *scanner) update(f logFile) *fileState {
	st := s.cache.Files[f.path]
	if st != nil && st.Engine == f.src.Engine() && st.Size == f.size && st.Mtime.Equal(f.mtime) {
		return st
	}
	if st == nil || !canResume(st, f) {
		if st != nil {
			s.release(f.path, st)
		}
		st = &fileState{Engine: f.src.Engine(), Head: headHash(f.path)}
	}
	if err := s.parse(f, st); err != nil {
		s.release(f.path, st)
		delete(s.cache.Files, f.path)
		return nil
	}
	st.Size, st.Mtime = f.size, f.mtime
	s.cache.Files[f.path] = st
	return st
}

func (s *scanner) release(path string, st *fileState) {
	for _, k := range st.Keys {
		if s.owners[k] == path {
			delete(s.owners, k)
		}
	}
}

// canResume reports whether a changed file is an append to what was parsed.
func canResume(st *fileState, f logFile) bool {
	return st.Engine == f.src.Engine() && f.size >= st.Size && f.size >= st.Offset &&
		st.Head == headHash(f.path)
}

// parse reads a file from st.Offset and folds its records into st.
func (s *scanner) parse(f logFile, st *fileState) error {
	fh, err := os.Open(f.path)
	if err != nil {
		return err
	}
	defer fh.Close()
	if _, err := fh.Seek(st.Offset, io.SeekStart); err != nil {
		return err
	}

	a := newAccumulator(st, func(k uint64) bool {
		o, ok := s.owners[k]
		return ok && o != f.path
	})
	lp := f.src.NewParser(st.State)
	br := bufio.NewReaderSize(fh, 64*1024)
	offset := st.Offset
	for {
		line, err := br.ReadBytes('\n')
		// A line without its newline is still being written; leave it for
		// the next scan rather than parsing half a record.
		if len(line) > 0 && line[len(line)-1] == '\n' {
			offset += int64(len(line))
			if r, ok := lp.Parse(line); ok {
				a.add(r)
			}
		}
		if err != nil {
			break
		}
	}
	st.Offset = offset
	st.State = lp.State()
	a.flush()
	for _, k := range st.Keys {
		s.owners[k] = f.path
	}
	return nil
}

// accumulator folds records into a file's buckets, applying dedupe.
type accumulator struct {
	st    *fileState
	idx   map[bucketKey]*bucket
	own   map[uint64]bool
	taken func(uint64) bool
}

func newAccumulator(st *fileState, taken func(uint64) bool) *accumulator {
	a := &accumulator{st: st, idx: map[bucketKey]*bucket{}, own: map[uint64]bool{}, taken: taken}
	for i := range st.Buckets {
		b := st.Buckets[i]
		if b.Slots == nil {
			b.Slots = map[uint16]int64{}
		}
		a.idx[b.key()] = &b
	}
	for _, k := range st.Keys {
		a.own[k] = true
	}
	return a
}

func (a *accumulator) add(r Record) {
	st := a.st
	if r.Key != 0 && r.Key == st.LastKey {
		if b := a.idx[st.LastBucket]; b != nil {
			b.Tokens.sub(st.LastTokens)
			b.Tokens.add(r.Tokens)
			b.Slots[st.LastSlot] += r.Tokens.Total() - st.LastTokens.Total()
			b.touch(r.Time)
		}
		st.LastTokens = r.Tokens
		return
	}
	if r.Key != 0 && (a.own[r.Key] || a.taken(r.Key)) {
		return
	}

	k := bucketKey{r.Time.Format(dateLayout), r.Model, r.RawProvider, r.Session}
	b := a.idx[k]
	if b == nil {
		b = &bucket{Date: k.Date, Engine: st.Engine, Model: k.Model, RawProvider: k.RawProvider, Session: k.Session,
			Slots: map[uint16]int64{}}
		a.idx[k] = b
	}
	slot := slotOf(r.Time)
	b.Tokens.add(r.Tokens)
	b.Slots[slot] += r.Tokens.Total()
	b.Requests++
	b.touch(r.Time)
	if r.Key != 0 {
		a.own[r.Key] = true
	}
	st.LastKey, st.LastTokens, st.LastBucket, st.LastSlot = r.Key, r.Tokens, k, slot
}

// slotOf is t's slot index from its local midnight.
func slotOf(t time.Time) uint16 {
	return uint16((t.Hour()*60 + t.Minute()) / int(slotLen/time.Minute))
}

func (b *bucket) touch(t time.Time) {
	if t.After(b.Last) {
		b.Last = t
	}
}

// flush writes the working set back into the state in a stable order.
func (a *accumulator) flush() {
	st := a.st
	st.Buckets = st.Buckets[:0]
	for _, b := range a.idx {
		st.Buckets = append(st.Buckets, *b)
	}
	sort.Slice(st.Buckets, func(i, j int) bool {
		x, y := st.Buckets[i].key(), st.Buckets[j].key()
		if x.Date != y.Date {
			return x.Date < y.Date
		}
		if x.Session != y.Session {
			return x.Session < y.Session
		}
		if x.Model != y.Model {
			return x.Model < y.Model
		}
		return x.RawProvider < y.RawProvider
	})
	st.Keys = st.Keys[:0]
	for k := range a.own {
		st.Keys = append(st.Keys, k)
	}
	sort.Slice(st.Keys, func(i, j int) bool { return st.Keys[i] < st.Keys[j] })
}

func headHash(path string) uint64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	buf := make([]byte, headLen)
	n, _ := io.ReadFull(f, buf)
	h := fnv.New64a()
	h.Write(buf[:n])
	return h.Sum64()
}

// hashKey builds a dedupe key; parts are NUL-separated so they cannot run together.
func hashKey(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

// parseTime parses a log timestamp into local time, so days split at local
// midnight rather than UTC midnight.
func parseTime(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.Local(), true
}
