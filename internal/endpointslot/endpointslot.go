// Package endpointslot bounds the HTTP calls in flight to one model server:
// the LLM backends' calls (internal/runner) and the embedding providers'
// (internal/embed) wait for the same slots, so a local server that answers
// one request at a time is sent one at a time. Where this build has file
// locks and the lock directory can be written, a slot is a locked file, so
// the chb processes on this machine share a server's slots; else each
// process counts only its own calls.
package endpointslot

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// servers holds, per server (Key), its slots. A server with no bound maps
// to nil.
var (
	mu      sync.Mutex
	servers = map[string]*server{}
)

// logw receives what the slots log: at a server's first call, whether its
// slots are shared with other processes, and if not, why; and once a wait
// for other processes passes noticeAfter.
var logw io.Writer = os.Stderr

// pollInterval is how often a call waiting for a slot another process holds
// tries again, so a slot freed there is taken within about this long.
const pollInterval = 25 * time.Millisecond

// stallPolls is how many polls in a row a waiter that cannot get next.lock
// must find a slot file free before it takes it. A queued waiter that is
// running takes a free file within one poll, so a file free this long has
// none that can take it: it is stopped (Ctrl-Z), or its bound does not
// reach that file.
var stallPolls = 4

// noticeAfter is how long a call waits for other processes' slots before
// the wait is logged, once per server.
var noticeAfter = 5 * time.Second

// server is one server's slots. sem bounds this process's calls in flight.
// With files set, each call also holds one of the lock files <files>-0.lock
// to <files>-<n-1>.lock, so the processes sharing them keep at most n calls
// in flight between them. A call waiting for one first holds
// <files>-next.lock, which every process's waiter must hold, so a slot a
// process frees goes to the waiter that queued, not back to the process
// that freed it. turn queues this process's waiters for next.lock.
type server struct {
	name    string
	sem     chan struct{}
	turn    chan struct{}
	files   string
	lost    atomic.Bool // the last lock file tried failed: calls count in this process only until one works
	noticed atomic.Bool // a wait for other processes' slots has been logged
}

// Limit is how many calls to endpoint may be in flight at once:
// HIVE_MAX_PARALLEL_ENDPOINT when it parses as an integer, clamped to
// 1-64 as HIVE_MAX_PARALLEL_NODES is; else 1 for an endpoint on this
// machine, whose server (Ollama, LM Studio) may answer one request at a
// time; else 0, no bound.
func Limit(endpoint string) int {
	def := 0
	if IsLoopbackURL(endpoint) {
		def = 1
	}
	n, err := strconv.Atoi(os.Getenv("HIVE_MAX_PARALLEL_ENDPOINT"))
	if err != nil {
		return def
	}
	return min(max(n, 1), 64)
}

// Acquire waits, within ctx, for a free slot at endpoint and returns its
// release and the server's bound, 0 when it has none and the call need not
// wait. Endpoints that reach one server share its slots (Key), and so do
// the processes that share its lock files. The bound is read at a server's
// first call and holds for the process. When ctx ends first, the error is
// ctx's, naming the processes that held the slots when it was waiting for
// other processes.
func Acquire(ctx context.Context, endpoint string) (release func(), n int, err error) {
	s := lookup(endpoint)
	if s == nil {
		return func() {}, 0, nil
	}
	n = cap(s.sem)
	select {
	case s.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, n, ctx.Err()
	}
	release, err = s.holdAcross(ctx)
	return release, n, err
}

// holdAcross completes the slot of a call that holds one of this process's
// slots: with lock files, it also takes one of them. It returns the release
// of what the call holds. A lock file that fails leaves the call holding
// this process's slot alone; when ctx ends first, the call gives that slot
// back and gets ctx's error.
func (s *server) holdAcross(ctx context.Context) (func(), error) {
	if s.files == "" {
		return s.releaseSem, nil
	}
	f, err := s.lockAcrossRetried(ctx)
	var lerr *lockError
	switch {
	case errors.As(err, &lerr):
		s.lose(lerr.err)
		return s.releaseSem, nil
	case err != nil:
		<-s.sem
		return nil, err
	}
	s.regain()
	return func() { unlock(f); <-s.sem }, nil
}

// releaseSem gives back a call's slot in this process.
func (s *server) releaseSem() { <-s.sem }

// lockAcrossRetried is lockAcross, tried once more after a lock file fails
// when the lock directory can be made again: a cache cleaner may have
// removed the directory.
func (s *server) lockAcrossRetried(ctx context.Context) (*os.File, error) {
	f, err := s.lockAcross(ctx)
	var lerr *lockError
	if errors.As(err, &lerr) && os.MkdirAll(filepath.Dir(s.files), 0o700) == nil {
		return s.lockAcross(ctx)
	}
	return f, err
}

// lookup is endpoint's server, set up at its first call; nil when it has no
// bound.
func lookup(endpoint string) *server {
	key := Key(endpoint)
	mu.Lock()
	defer mu.Unlock()
	s, ok := servers[key]
	if !ok {
		if n := Limit(endpoint); n > 0 {
			s = newServer(endpoint, key, n)
		}
		servers[key] = s
	}
	return s
}

// newServer sets up key's n slots, shared through lock files when it can,
// and logs which applies.
func newServer(endpoint, key string, n int) *server {
	s := &server{name: key, sem: make(chan struct{}, n), turn: make(chan struct{}, 1)}
	if u, err := url.Parse(endpoint); err != nil || u.Host == "" {
		// Key is then the endpoint's raw text, which may hold a credential.
		s.name = "an endpoint with no host"
	}
	files, err := lockFiles(key)
	if err != nil {
		fmt.Fprintf(logw, "endpoint slots: %s: %d call(s) in flight at a time from this process; other chb processes are not counted (no lock files: %v)\n", s.name, n, err)
		return s
	}
	s.files = files
	fmt.Fprintf(logw, "endpoint slots: %s: %d call(s) in flight at a time, shared with other chb processes (lock files %s-*.lock)\n", s.name, n, files)
	return s
}

// lockFiles is the path prefix of key's lock files in slotDir, named by a
// hash of key rather than key, which for an endpoint that does not parse is
// its raw text. It checks first that this build has file locks and that
// they work in that directory (checkLocks).
func lockFiles(key string) (string, error) {
	if errNoFileLocks != nil {
		return "", errNoFileLocks
	}
	dir, err := lockDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])), nil
}

// lockDir is slotDir, made when it is missing, once checkLocks finds that
// file locks work there.
func lockDir() (string, error) {
	dir, err := slotDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := checkLocks(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// slotDir is HIVE_ENDPOINT_SLOT_DIR, with a leading ~ as the user's home
// directory, else <user cache dir>/chb/slots. A relative path is refused:
// each process would find it from its own working directory, so processes
// started in different directories would not share slots.
func slotDir() (string, error) {
	dir := os.Getenv("HIVE_ENDPOINT_SLOT_DIR")
	if dir == "" {
		return defaultSlotDir()
	}
	dir, err := expandHome(dir)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("HIVE_ENDPOINT_SLOT_DIR=%s is not an absolute path", dir)
	}
	return dir, nil
}

// defaultSlotDir is <user cache dir>/chb/slots.
func defaultSlotDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "chb", "slots"), nil
}

// expandHome is dir with a leading ~ as the user's home directory.
func expandHome(dir string) (string, error) {
	if dir != "~" && !strings.HasPrefix(dir, "~/") {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dir[1:]), nil
}

// checkLocks checks, on a new file in dir, that a file there can be locked,
// and that a second open of it in this process cannot lock it as well. The
// slots rest on that: flock's locks belong to an open file on a local disk,
// but where they belong to the process, as when Linux emulates flock with
// POSIX locks on NFS, two calls in one process could hold one slot file.
func checkLocks(dir string) error {
	f, err := os.CreateTemp(dir, "check-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := flock(f); err != nil {
		return err
	}
	return checkSecondOpen(f.Name(), dir)
}

// checkSecondOpen checks that a second open of path, a file in dir that an
// open in this process holds locked, cannot lock it as well.
func checkSecondOpen(path, dir string) error {
	g, err := os.Open(path)
	if err != nil {
		return err
	}
	defer g.Close()
	ok, err := flock(g)
	if ok {
		return fmt.Errorf("a second open of a locked file in %s locks it too, as on a network filesystem", dir)
	}
	return err
}

// lockAcross takes, within ctx, one of s's slot files. This process's
// waiters queue for next.lock one at a time (turn), and the one holding it
// takes the first slot file free. A waiter that cannot get next.lock, which
// another process's waiter holds, still tries the slot files, and takes one
// it has found free stallPolls polls in a row. When ctx ends first, it
// returns ctx's error naming the processes that held the slots (waitError),
// and a *lockError when a file cannot be opened or locked. It lets go of
// next.lock and of its turn however it returns.
func (s *server) lockAcross(ctx context.Context) (*os.File, error) {
	w := &slotWaiter{s: s, start: time.Now()}
	select {
	case s.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, s.waitError(ctx.Err())
	}
	defer func() { <-s.turn }()
	defer w.letGoNext()
	return w.wait(ctx)
}

// slotWaiter is one call's wait in lockAcross: its server, when it began,
// the next.lock it holds (nil until it gets it), and how many polls in a row
// it has found a slot file free without being allowed to take it.
type slotWaiter struct {
	s     *server
	start time.Time
	next  *os.File
	free  int
}

// letGoNext lets go of next.lock when the waiter holds it.
func (w *slotWaiter) letGoNext() {
	if w.next != nil {
		unlock(w.next)
	}
}

// wait polls until it takes a slot file, a lock file fails or ctx ends.
func (w *slotWaiter) wait(ctx context.Context) (*os.File, error) {
	for {
		f, err := w.round(ctx)
		if f != nil || err != nil {
			return f, err
		}
	}
}

// round is one poll and, when it takes no slot file, the pause before the
// next.
func (w *slotWaiter) round(ctx context.Context) (*os.File, error) {
	if f, err := w.poll(); f != nil || err != nil {
		return f, err
	}
	return nil, w.pause(ctx)
}

// poll takes next.lock unless the waiter holds it or another process's
// waiter does, then tries the slot files. It returns the slot file it takes
// (keep), nil when it takes none, and a *lockError when a file cannot be
// opened or locked.
func (w *slotWaiter) poll() (*os.File, error) {
	if err := w.tryNext(); err != nil {
		return nil, &lockError{err}
	}
	f, err := w.s.trySlots()
	if err != nil {
		return nil, &lockError{err}
	}
	return w.keep(f), nil
}

// tryNext takes next.lock unless the waiter holds it already. It is left
// unheld while another process's waiter holds it.
func (w *slotWaiter) tryNext() error {
	if w.next != nil {
		return nil
	}
	f, err := tryLock(w.s.files + "-next.lock")
	if err != nil {
		return err
	}
	w.next = f
	return nil
}

// keep takes f, the slot file the poll found free, when the waiter may
// (mayTake), and returns it; else it lets f go and returns nil. A poll that
// found none resets the count of free polls.
func (w *slotWaiter) keep(f *os.File) *os.File {
	if f == nil {
		w.free = 0
		return nil
	}
	if !w.mayTake() {
		w.free++
		unlock(f)
		return nil
	}
	// The pid lets a process waiting for this slot name its holder.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
	return f
}

// mayTake reports whether the waiter may take a slot file it found free: it
// holds next.lock, or this is the stallPolls-th poll in a row to find one
// free.
func (w *slotWaiter) mayTake() bool {
	return w.next != nil || w.free+1 >= stallPolls
}

// pause notes a long wait, then waits a poll interval. When ctx ends first
// it returns ctx's error naming the processes that held the slots.
func (w *slotWaiter) pause(ctx context.Context) error {
	w.noteLongWait()
	t := time.NewTimer(pollInterval)
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		t.Stop()
		return w.s.waitError(ctx.Err())
	}
}

// noteLongWait logs a wait that has passed noticeAfter, once per server.
func (w *slotWaiter) noteLongWait() {
	if time.Since(w.start) >= noticeAfter && w.s.noticed.CompareAndSwap(false, true) {
		fmt.Fprintf(logw, "endpoint slots: %s: waiting for a slot %s; the call's timeout starts once it has one\n", w.s.name, w.s.holders())
	}
}

// trySlots takes the first of s's slot files free, or returns nil.
func (s *server) trySlots() (*os.File, error) {
	for i := range cap(s.sem) {
		if f, err := tryLock(s.slotFile(i)); f != nil || err != nil {
			return f, err
		}
	}
	return nil, nil
}

func (s *server) slotFile(i int) string { return s.files + "-" + strconv.Itoa(i) + ".lock" }

// holders says who holds s's slot files: "other chb processes hold", with
// the pids their holders wrote, other than this process's.
func (s *server) holders() string {
	pids := s.holderPIDs()
	if len(pids) == 0 {
		return "other chb processes hold"
	}
	return "other chb processes hold (pid " + strings.Join(pids, ", ") + ")"
}

// holderPIDs is the pids the holders of s's slot files wrote, each once,
// other than this process's.
func (s *server) holderPIDs() []string {
	var pids []string
	for i := range cap(s.sem) {
		if p, ok := otherPID(s.slotFile(i)); ok && !slices.Contains(pids, p) {
			pids = append(pids, p)
		}
	}
	return pids
}

// otherPID is the pid written in the slot file at path; ok is false when it
// holds none, or this process's.
func otherPID(path string) (string, bool) {
	b, _ := os.ReadFile(path)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return strconv.Itoa(pid), err == nil && pid != os.Getpid()
}

// waitError is err, ctx's, for a call that was waiting for other processes'
// slots, naming them.
func (s *server) waitError(err error) error {
	return fmt.Errorf("waiting for a slot %s: %w", s.holders(), err)
}

// lose makes a call's slot this process's own, after a lock file failed,
// and logs it unless the lock file before it had failed too.
func (s *server) lose(err error) {
	if s.lost.CompareAndSwap(false, true) {
		fmt.Fprintf(logw, "endpoint slots: %s: a lock file failed (%v); until one works again, calls are counted in this process only\n", s.name, err)
	}
}

// regain logs that a lock file works again after one failed.
func (s *server) regain() {
	if s.lost.CompareAndSwap(true, false) {
		fmt.Fprintf(logw, "endpoint slots: %s: the lock files work again; calls are shared with other chb processes\n", s.name)
	}
}

// lockError is a lock file that could not be opened or locked.
type lockError struct{ err error }

func (e *lockError) Error() string { return e.err.Error() }

// tryLock opens path, creating it, and locks it (lockOpen).
func tryLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	return lockOpen(f)
}

// lockOpen locks f without waiting. It returns f locked, or closes it and
// returns nil and no error when another open of the file holds the lock. A
// holder removes its file before it lets go (unlock), so a lock taken on a
// file no longer at f's path is let go and counts as held.
func lockOpen(f *os.File) (*os.File, error) {
	ok, err := flock(f)
	if ok && atItsPath(f) {
		return f, nil
	}
	f.Close()
	return nil, err
}

// atItsPath reports whether f is the file now at its path.
func atItsPath(f *os.File) bool {
	fi, ferr := f.Stat()
	pi, perr := os.Stat(f.Name())
	return ferr == nil && perr == nil && os.SameFile(fi, pi)
}

// closeFile closes the file unlock lets go of. A test replaces it to check
// that the file's path is gone first.
var closeFile = (*os.File).Close

// unlock lets go of a file tryLock locked, removing it first. Closed first,
// it could be locked by a waiter that had it open, then removed from under
// that waiter, and a new file at its path would give the slot two holders.
// The kernel lets go of a process's locks when it exits, however it exits.
func unlock(f *os.File) {
	os.Remove(f.Name())
	closeFile(f)
}

// Key names the server an endpoint's calls reach: its host and port, the
// port defaulting by scheme, with every name for this machine as one host.
// So http://localhost:11434/v1, which the OpenAI-compatible backend calls,
// and http://127.0.0.1:11434, which the Anthropic one and the Ollama
// embedding provider call, are one Ollama and share its slots. An endpoint
// that does not parse is its own key.
func Key(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return endpoint
	}
	host := strings.ToLower(u.Hostname())
	if isLoopbackHost(host) {
		host = "localhost"
	}
	return net.JoinHostPort(host, portOf(u))
}

// defaultPorts is the port each scheme implies.
var defaultPorts = map[string]string{"http": "80", "https": "443"}

// portOf is u's port, defaulting by its scheme.
func portOf(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return defaultPorts[strings.ToLower(u.Scheme)]
}

// IsLoopbackURL reports whether endpoint's host is this machine
// (isLoopbackHost).
func IsLoopbackURL(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	return isLoopbackHost(u.Hostname())
}

// isLoopbackHost reports whether host is this machine: localhost, a loopback
// address, or the unspecified address (0.0.0.0, ::), which a client's call
// reaches this machine on, and which an OLLAMA_HOST=0.0.0.0 setup often
// copies into a base URL.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}
