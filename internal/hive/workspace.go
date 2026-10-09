package hive

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Chubby-Honey-Bee/hive/internal/db"
)

// Lookup is what a hive command knows when it chooses its database
// (hive.md § One hive per database).
type Lookup struct {
	Project string
	// Named is the database the caller named, open: --db, else
	// HIVE_DB_PATH, else the default path; a server's own store.
	Named     *db.Store
	NamedPath string
	// Pinned says the caller named it with --db for this one command. A
	// pinned database that hosts another project's hive is refused rather
	// than left for the project's workspace database.
	Pinned bool
	// CreateDB lets the rule create the project's workspace database when
	// it chooses it and it does not exist: `hive init`, and a run of a
	// workflow that drives the hive. Every other command refuses a missing
	// one.
	CreateDB bool
}

// Resolved is the database a hive command uses.
type Resolved struct {
	Store *db.Store
	Path  string
	// Named is the path the caller named. HostedBy is the project whose
	// hive it hosts when Path is not Named, else "".
	Named    string
	HostedBy string
	// CreatedDB says this call created the workspace database.
	CreatedDB bool
	opened    bool
}

// Close closes the store when Resolve opened it. The named store is the
// caller's and stays open.
func (r *Resolved) Close() error {
	if r.opened {
		return r.Store.Close()
	}
	return nil
}

// NoWorkspaceDBError is the refusal when the rule chose the project's
// workspace database, it does not exist, and the command does not create
// one.
type NoWorkspaceDBError struct {
	Project, Named, HostedBy, Path string
}

// Error names the project, the database that hosts another hive, and the
// workspace database that does not exist.
func (e *NoWorkspaceDBError) Error() string {
	return fmt.Sprintf("no hive for project %q: %s hosts hive project %q, and %s does not exist (chb hive init --project %s creates it)",
		e.Project, e.Named, e.HostedBy, e.Path, e.Project)
}

// OtherHiveError is the refusal when a database the command cannot leave
// hosts another project's hive. Use, when set, is the path the project uses
// instead.
type OtherHiveError struct {
	Project, Other, Path, Use string
}

// Error names the database, the project whose hive it hosts and, when set,
// the database the project uses instead.
func (e *OtherHiveError) Error() string {
	msg := fmt.Sprintf("%s hosts hive project %q, and one database holds one hive, so it cannot hold project %q", e.Path, e.Other, e.Project)
	if e.Use != "" {
		msg += fmt.Sprintf("; project %q uses %s (pass --db %s, or drop --db and chb hive init --project %s creates it)",
			e.Project, e.Use, e.Use, e.Project)
	}
	return msg
}

// WorkspaceRoot is the directory whose workspace/ folder holds the named
// database: the parent of the `workspace` directory when the database is
// at <root>/workspace/hive.db (the default path) or
// <root>/workspace/<name>/hive.db (what `chb init <name>` makes). A
// database in neither layout has no workspace/ folder to anchor on, and the
// root is the current directory, where `chb init` would make one.
func WorkspaceRoot(named string) string {
	dir := filepath.Dir(named)
	if filepath.Base(dir) == "workspace" {
		return filepath.Dir(dir)
	}
	if up := filepath.Dir(dir); filepath.Base(up) == "workspace" {
		return filepath.Dir(up)
	}
	return "."
}

// WorkspacePath is the project's workspace database under the named
// database's workspace root.
func WorkspacePath(named, project string) string {
	return filepath.Join(WorkspaceRoot(named), "workspace", project, "hive.db")
}

// Resolve chooses the database a hive command for l.Project uses: the named
// database when it hosts no other project's hive, else the project's
// workspace database. It is the one place the rule lives; every surface
// that reaches a hive calls it.
func Resolve(l Lookup) (*Resolved, error) {
	other, err := ExistingProject(l.Named, l.Project)
	if err != nil {
		return nil, err
	}
	if other == "" {
		return &Resolved{Store: l.Named, Path: l.NamedPath, Named: l.NamedPath}, nil
	}
	return resolveWorkspace(l, other)
}

// resolveWorkspace is the rule's second branch: the named database hosts
// other's hive, so the project uses its workspace database.
func resolveWorkspace(l Lookup, other string) (*Resolved, error) {
	path := WorkspacePath(l.NamedPath, l.Project)
	if err := workspaceRefusal(l, other, path); err != nil {
		return nil, err
	}
	created, err := ensureWorkspaceDB(l, other, path)
	if err != nil {
		return nil, err
	}
	st, err := openWorkspaceStore(path, l.Project)
	if err != nil {
		return nil, err
	}
	return &Resolved{Store: st, Path: path, Named: l.NamedPath, HostedBy: other, CreatedDB: created, opened: true}, nil
}

// workspaceRefusal refuses a project that cannot name a workspace
// directory, and a pinned database, which the command cannot leave.
func workspaceRefusal(l Lookup, other, path string) error {
	if !validWorkspaceName(l.Project) {
		return fmt.Errorf("%s hosts hive project %q, and project %q cannot name a workspace directory", l.NamedPath, other, l.Project)
	}
	if l.Pinned {
		return &OtherHiveError{Project: l.Project, Other: other, Path: l.NamedPath, Use: path}
	}
	return nil
}

// validWorkspaceName reports whether project can name a workspace
// directory: one path element, not hidden.
func validWorkspaceName(project string) bool {
	return project != "" && project == filepath.Base(project) && !strings.HasPrefix(project, ".")
}

// ensureWorkspaceDB makes sure the workspace database's directory exists
// when the database does not, and reports whether this call is creating the
// database. A missing one is refused unless the lookup may create it.
func ensureWorkspaceDB(l Lookup, other, path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	if err := createWorkspaceDir(l, other, path); err != nil {
		return false, err
	}
	return true, nil
}

// createWorkspaceDir makes the missing workspace database's directory, when
// the lookup may create the database.
func createWorkspaceDir(l Lookup, other, path string) error {
	if !l.CreateDB {
		return &NoWorkspaceDBError{Project: l.Project, Named: l.NamedPath, HostedBy: other, Path: path}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create workspace for project %q: %w", l.Project, err)
	}
	return nil
}

// openWorkspaceStore opens and initialises the workspace database. The
// workspace database is where the rule ends: one that hosts another
// project's hive is refused.
func openWorkspaceStore(path, project string) (*db.Store, error) {
	st, err := db.NewStore(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := st.Init(); err != nil {
		st.Close()
		return nil, fmt.Errorf("init %s: %w", path, err)
	}
	if err := refuseOtherHive(st, project, path); err != nil {
		st.Close()
		return nil, err
	}
	return st, nil
}

// refuseOtherHive refuses a database that hosts another project's hive.
func refuseOtherHive(st *db.Store, project, path string) error {
	taken, err := ExistingProject(st, project)
	if err != nil {
		return err
	}
	if taken != "" {
		return &OtherHiveError{Project: project, Other: taken, Path: path}
	}
	return nil
}

// Init records the project's hive (InitProject) in the database the rule
// chooses, creating the workspace database when it is missing. created says
// the hive_state row is new. The named database is checked by InitProject's
// one conditional insert, not by a read before it, so two inits of different
// projects at once leave one hive there and send the other on to its
// workspace database, as if the first had been there all along.
func Init(l Lookup) (*Resolved, bool, error) {
	created, other, err := InitProject(l.Named, l.Project)
	if err != nil {
		return nil, false, err
	}
	if other == "" {
		return &Resolved{Store: l.Named, Path: l.NamedPath, Named: l.NamedPath}, created, nil
	}
	return initWorkspace(l, other)
}

// initWorkspace records the project's hive in its workspace database,
// creating the database when it is missing.
func initWorkspace(l Lookup, other string) (*Resolved, bool, error) {
	l.CreateDB = true
	r, err := resolveWorkspace(l, other)
	if err != nil {
		return nil, false, err
	}
	created, err := initResolved(r, l.Project)
	if err != nil {
		r.Close()
		return nil, false, err
	}
	return r, created, nil
}

// initResolved records project's hive in r's database. A database another
// project's hive took first is refused.
func initResolved(r *Resolved, project string) (bool, error) {
	created, other, err := InitProject(r.Store, project)
	if err != nil {
		return false, err
	}
	if other != "" {
		return false, &OtherHiveError{Project: project, Other: other, Path: r.Path}
	}
	return created, nil
}
