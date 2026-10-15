package dbconfig

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func sqliteSelection(t *testing.T) Selection {
	t.Helper()
	s, err := ResolveStorage(StorageInputs{FreshInstall: true, BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := NewSelection(s)
	if err != nil {
		t.Fatal(err)
	}
	return selection
}

func TestSelectionMissingAndAuthoritativeAdoption(t *testing.T) {
	want := sqliteSelection(t)
	path := SelectionPath(want.StateDir)
	if _, _, err := OpenSelection(path); !errors.Is(err, ErrSelectionMissing) {
		t.Fatalf("missing record=%v", err)
	}
	got, err := AdoptSelection(path, want)
	if err != nil {
		t.Fatal(err)
	}
	if got.Generation != want.Generation {
		t.Fatal("initial generation changed")
	}
	raw := StorageInputs{BaseDir: t.TempDir(), Backend: Postgres, EnvBackend: Postgres, FreshInstall: true, DBPath: "obsolete.db", UsersDBPath: "obsolete-users.db", StateDir: "old-state", DatabaseURL: "inactive-private-url"}
	raw, err = got.ApplyTo(raw)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if raw.FreshInstall || raw.ExistingBackend != SQLite || resolved.Backend != SQLite || resolved.DBPath != want.Telemetry.SQLitePath || resolved.UsersDBPath != want.Accounts.SQLitePath || resolved.StateDir != want.StateDir {
		t.Fatal("bootstrap settings overrode installed selection")
	}
	duplicate := want
	duplicate.Generation = strings.Repeat("a", 32)
	got, err = AdoptSelection(path, duplicate)
	if err != nil || got.Generation != want.Generation {
		t.Fatalf("idempotent adoption: %v", err)
	}
	duplicate.Telemetry.SQLitePath = filepath.Join(want.StateDir, "other.db")
	if _, err := AdoptSelection(path, duplicate); !errors.Is(err, ErrSelectionChanged) {
		t.Fatalf("adoption changed targets: %v", err)
	}
}

func TestSelectionPostgresCredentialsAreNotPersisted(t *testing.T) {
	t.Setenv("PGPORT", "")
	t.Setenv("PGOPTIONS", "")
	t.Setenv("PGSERVICE", "")
	base := t.TempDir()
	storage, err := ResolveStorage(StorageInputs{BaseDir: base, Backend: Postgres, DatabaseURL: "postgres://first:never-record-password@localhost/telemetry?sslmode=require&search_path=public", UsersDatabaseURL: "postgres://accounts:never-record-password@localhost/accounts"})
	if err != nil {
		t.Fatal(err)
	}
	selected, err := NewSelection(storage)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"never-record-password", "sslmode", "postgres://", "first:"} {
		if strings.Contains(string(data), private) {
			t.Fatal("credentials or TLS settings persisted")
		}
	}
	raw := StorageInputs{BaseDir: base, Backend: SQLite, DBPath: "inactive.db", DatabaseURL: "postgres://rotated:replacement-secret@localhost:5432/telemetry?sslmode=verify-full", UsersDatabaseURL: "postgres://rotated:replacement-secret@localhost/accounts?sslmode=require"}
	raw, err = selected.ApplyTo(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw.ReaderDatabaseURL, "replacement-secret") || !strings.Contains(raw.ReaderDatabaseURL, "sslmode=verify-full") || !strings.Contains(raw.ReaderDatabaseURL, "search_path=") {
		t.Fatal("rotation lost credentials/TLS or left schema unbound")
	}
	raw.ReaderDatabaseURL = "postgres://reader:secret@localhost/another"
	if _, err := selected.ApplyTo(raw); !errors.Is(err, ErrSelectionChanged) {
		t.Fatalf("active PG retarget accepted: %v", err)
	}
}

func TestSelectionPinsRecordedPortAndRefusesAmbiguousBootstrap(t *testing.T) {
	t.Setenv("PGPORT", "")
	t.Setenv("PGOPTIONS", "")
	t.Setenv("PGSERVICE", "")
	base := t.TempDir()
	selected, err := NewSelection(Storage{Backend: Postgres, StateDir: base, WriterDatabaseURL: "postgres://owner:secret@localhost/data"})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PGPORT", "6543")
	bound, err := selected.ApplyTo(StorageInputs{BaseDir: base, DatabaseURL: "postgres://rotated:secret@localhost/data"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bound.WriterDatabaseURL, "localhost:5432/") {
		t.Fatal("recorded port was left to ambient PGPORT")
	}
	if _, err := NewSelection(Storage{Backend: Postgres, StateDir: base, WriterDatabaseURL: "postgres://owner:secret@localhost/data"}); err == nil {
		t.Fatal("ambiguous implicit bootstrap port accepted")
	}
	if _, err := NewSelection(Storage{Backend: Postgres, StateDir: base, WriterDatabaseURL: "postgres://owner:secret@localhost:5432/data"}); err != nil {
		t.Fatal("explicit port refused", err)
	}
	for _, key := range []string{"PGOPTIONS", "PGSERVICE"} {
		t.Setenv(key, "ambiguous")
		if _, err := NewSelection(Storage{Backend: Postgres, StateDir: base, WriterDatabaseURL: "postgres://owner:secret@localhost:5432/data"}); err == nil {
			t.Fatal("ambient target options accepted", key)
		}
		t.Setenv(key, "")
	}
}

func TestRecordedAccountAbsenceDoesNotDefaultOrActivateRawSettings(t *testing.T) {
	base := t.TempDir()
	selected, err := NewSelection(Storage{Backend: SQLite, StateDir: base, DBPath: filepath.Join(base, "telemetry.sqlite")})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Accounts != nil {
		t.Fatal("absent accounts recorded as an initialized target")
	}
	raw, err := selected.ApplyTo(StorageInputs{BaseDir: base, UsersDBPath: "must-not-create.db", UsersDatabaseURL: "inactive"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveStorage(raw)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.UsersDBPath != "" {
		t.Fatal("recorded account absence defaulted to a new file")
	}
	bootstrap, err := ResolveStorage(StorageInputs{BaseDir: base, Backend: SQLite, DBPath: filepath.Join(base, "telemetry.sqlite")})
	if err != nil || bootstrap.UsersDBPath == "" {
		t.Fatal("bootstrap account default lost", err)
	}
	pg, err := NewSelection(Storage{Backend: Postgres, StateDir: base, WriterDatabaseURL: "postgres://owner:secret@localhost:5432/data"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = pg.ApplyTo(StorageInputs{BaseDir: base, DatabaseURL: "postgres://writer:secret@localhost:5432/data", UsersDatabaseURL: "inactive-not-selected", ApprovedChannelsDatabaseURL: "inactive-not-selected"})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = ResolveStorage(raw)
	if err != nil || resolved.UsersDatabaseURL != "" || resolved.ApprovedChannelsDatabaseURL != "" {
		t.Fatal("raw credentials activated uninitialized accounts", err)
	}
}

func TestSelectionRuntimeLeasesAndCrashRecovery(t *testing.T) {
	current := sqliteSelection(t)
	path := SelectionPath(current.StateDir)
	if _, err := AdoptSelection(path, current); err != nil {
		t.Fatal(err)
	}
	_, a, err := OpenSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	_, b, err := OpenSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BeginSelectionUpdate(path, current.Generation); !errors.Is(err, ErrSelectionBusy) {
		t.Fatalf("runtime lease did not fence switch: %v", err)
	}
	a.Close()
	b.Close()
	u, err := BeginSelectionUpdate(path, current.Generation)
	if err != nil {
		t.Fatal(err)
	}
	id := u.ID()
	if _, _, err := OpenSelection(path); err == nil {
		t.Fatal("runtime entered active switch")
	}
	if _, err := ResumeSelectionUpdate(path, id); !errors.Is(err, ErrSelectionBusy) {
		t.Fatalf("concurrent resume=%v", err)
	}
	if err := u.Close(); err != nil {
		t.Fatal(err)
	} // process-exit behavior: journal remains
	if _, _, err := OpenSelection(path); !errors.Is(err, ErrSelectionInProgress) {
		t.Fatalf("crash journal ignored: %v", err)
	}
	if _, err := ResumeSelectionUpdate(path, "wrong-job"); err == nil {
		t.Fatal("wrong resume accepted")
	}
	u, err = ResumeSelectionUpdate(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if u.Current().Generation != current.Generation {
		t.Fatal("resume changed source generation")
	}
	if err := u.Abort(); err != nil {
		t.Fatal(err)
	}
	u.Close()
	got, lease, err := OpenSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if got.Generation != current.Generation {
		t.Fatal("abort changed selection")
	}
}

func TestSelectionCommitAndFailedCommitRemainRecoverable(t *testing.T) {
	old := sqliteSelection(t)
	path := SelectionPath(old.StateDir)
	if _, err := AdoptSelection(path, old); err != nil {
		t.Fatal(err)
	}
	u, err := BeginSelectionUpdate(path, old.Generation)
	if err != nil {
		t.Fatal(err)
	}
	next := old
	next.Generation = strings.Repeat("b", 32)
	next.Telemetry.SQLitePath = filepath.Join(old.StateDir, "staged.db")
	bad := next
	bad.StateDir = t.TempDir()
	if replaced, err := u.Commit(bad); err == nil || replaced {
		t.Fatal("switch moved state directory")
	}
	if err := u.Stage(next); err != nil {
		t.Fatal(err)
	}
	if u.Target().Generation != next.Generation {
		t.Fatal("staged target absent")
	}
	view := u.Target()
	view.Accounts.SQLitePath = filepath.Join(old.StateDir, "mutated-view.db")
	if u.Target().Accounts.SQLitePath != next.Accounts.SQLitePath {
		t.Fatal("caller mutated staged target through a shared pointer")
	}
	changed := next
	changed.Telemetry.SQLitePath = filepath.Join(old.StateDir, "substituted.db")
	if err := u.Stage(changed); !errors.Is(err, ErrSelectionChanged) {
		t.Fatal("staged target changed", err)
	}
	u.afterReplace = func() error { return errors.New("injected crash after replacement") }
	if replaced, err := u.Commit(next); err == nil || !replaced {
		t.Fatalf("post-replace result=%v %v", replaced, err)
	}
	id := u.ID()
	u.Close()
	if _, _, err := OpenSelection(path); !errors.Is(err, ErrSelectionInProgress) {
		t.Fatal("post-replace failure allowed startup")
	}
	u, err = ResumeSelectionUpdate(path, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Abort(); err == nil {
		t.Fatal("abort rolled back a changed selection")
	}
	if replaced, err := u.Commit(Selection{}); err == nil || !replaced {
		t.Fatal("failed retry concealed an already replaced selection")
	}
	if replaced, err := u.Commit(next); err != nil || !replaced {
		t.Fatalf("verified retry failed: %v %v", replaced, err)
	}
	u.Close()
	got, lease, err := OpenSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if got.Generation != next.Generation || got.Telemetry.SQLitePath != next.Telemetry.SQLitePath {
		t.Fatal("retry did not publish exact verified target")
	}
}

func TestSelectionStatusAndNativeProcessCrash(t *testing.T) {
	s := sqliteSelection(t)
	path := SelectionPath(s.StateDir)
	status, err := InspectSelection(path)
	if err != nil || status.State != "unrecorded" || status.Backend != "" {
		t.Fatalf("missing status: %+v %v", status, err)
	}
	if _, err := AdoptSelection(path, s); err != nil {
		t.Fatal(err)
	}
	status, err = InspectSelection(path)
	if err != nil || status.State != "ready" || status.Generation != s.Generation || status.Backend != SQLite {
		t.Fatalf("ready status: %+v %v", status, err)
	}
	if status.StateDir != s.StateDir || status.Telemetry == nil || status.Telemetry.SQLitePath != s.Telemetry.SQLitePath || status.Accounts == nil || status.Accounts.SQLitePath != s.Accounts.SQLitePath {
		t.Fatal("status omitted selected targets needed for native backups")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestSelectionCrashChild$")
	ready := filepath.Join(t.TempDir(), "ready")
	child.Env = append(os.Environ(), "CORESCOPE_SELECTION_CRASH_TEST="+path, "CORESCOPE_SELECTION_CRASH_READY="+ready)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { child.Process.Kill(); child.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not acquire native lease")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, _, err := OpenSelection(path); !errors.Is(err, ErrSelectionBusy) {
		t.Fatalf("another process entered switch lease: %v", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	status, err = InspectSelection(path)
	if err != nil || status.State != "pending" || status.JobID == "" || status.SourceBackend != SQLite {
		t.Fatalf("crashed process status: %+v %v", status, err)
	}
	if _, _, err := OpenSelection(path); !errors.Is(err, ErrSelectionInProgress) {
		t.Fatalf("crash did not retain startup gate: %v", err)
	}
	u, err := ResumeSelectionUpdate(path, status.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if err := u.Abort(); err != nil {
		t.Fatal(err)
	}
	u.Close()
	if err := os.WriteFile(path+".switch.json", []byte(`{"version":99}`), 0600); err != nil {
		t.Fatal(err)
	}
	status, err = InspectSelection(path)
	if err == nil || status.State != "corrupt" {
		t.Fatalf("corrupt status: %+v %v", status, err)
	}
}

func TestSelectionCrashChild(t *testing.T) {
	path := os.Getenv("CORESCOPE_SELECTION_CRASH_TEST")
	if path == "" {
		return
	}
	s, err := ReadSelection(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := BeginSelectionUpdate(path, s.Generation)
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	if err := os.WriteFile(os.Getenv("CORESCOPE_SELECTION_CRASH_READY"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Minute)
	t.Fatal("parent did not terminate child")
}

func TestSelectionRefusesHiddenPostgresRetargeting(t *testing.T) {
	for _, query := range []string{"host=other", "hostaddr=127.0.0.2", "port=5000", "dbname=other", "database=other", "service=other", "servicefile=other", "options=-csearch_path%3Dother"} {
		if _, err := postgresTarget("postgres://role:secret@localhost/telemetry?" + query); err == nil {
			t.Errorf("accepted target override %s", query)
		}
	}
}

func TestSelectionStatusDoesNotBlockAtomicReplacement(t *testing.T) {
	selected := sqliteSelection(t)
	path := SelectionPath(selected.StateDir)
	if _, err := AdoptSelection(path, selected); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	fail := make(chan error, 1)
	var reader sync.WaitGroup
	reader.Add(1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			status, err := InspectSelection(path)
			if err != nil || (status.State != "ready" && status.State != "pending") {
				if err == nil {
					err = errors.New("unexpected status during update")
				}
				select {
				case fail <- err:
				default:
				}
				return
			}
		}
	}()
	defer func() {
		close(stop)
		reader.Wait()
		select {
		case err := <-fail:
			t.Error(err)
		default:
		}
	}()
	for i := 0; i < 30; i++ {
		update, err := BeginSelectionUpdate(path, selected.Generation)
		if err != nil {
			t.Fatal(err)
		}
		next := cloneSelection(selected)
		next.Generation, err = newSelectionID()
		if err != nil {
			update.Close()
			t.Fatal(err)
		}
		if _, err := update.Commit(next); err != nil {
			update.Close()
			t.Fatal(err)
		}
		update.Close()
		selected = next
	}
}

func TestSelectionCorruptionDoesNotDefault(t *testing.T) {
	s := sqliteSelection(t)
	path := SelectionPath(s.StateDir)
	if _, err := AdoptSelection(path, s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99,"backend":"sqlite"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := OpenSelection(path); err == nil || errors.Is(err, ErrSelectionMissing) {
		t.Fatal("invalid record treated as fresh")
	}
}
