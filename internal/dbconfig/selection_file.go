package dbconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sync"
)

const maxSelectionBytes = 64 * 1024

type selectionLease struct {
	file *os.File
	once sync.Once
	err  error
}

func (l *selectionLease) Close() error {
	l.once.Do(func() { l.err = errors.Join(unlockSelectionFile(l.file), l.file.Close()) })
	return l.err
}

func acquireSelectionLease(path string, exclusive bool) (*selectionLease, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("selection path must be absolute and normalized")
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrSelectionMissing
	}
	if err != nil {
		return nil, err
	}
	if err = lockSelectionFile(f, exclusive); err != nil {
		f.Close()
		return nil, err
	}
	return &selectionLease{file: f}, nil
}

func readSelectionJSON(path string, out any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSelectionBytes {
		return errors.New("invalid selection metadata file")
	}
	f, err := openSelectionRead(path)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSelectionBytes+1))
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if len(data) > maxSelectionBytes {
		return errors.New("selection metadata exceeds its bound")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return errors.New("invalid selection metadata JSON")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("unexpected trailing selection metadata")
	}
	return nil
}

func readSelected(path string) (Selection, error) {
	var s Selection
	if err := readSelectionJSON(path, &s); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, ErrSelectionMissing
		}
		return s, err
	}
	if err := s.validate(); err != nil {
		return Selection{}, err
	}
	if SelectionPath(s.StateDir) != path {
		return Selection{}, errors.New("selection state directory does not match its discovery path")
	}
	return s, nil
}

func selectionJournalError(path string) error {
	_, err := os.Lstat(path + ".switch.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var journal selectionJournal
	if readSelectionJSON(path+".switch.json", &journal) == nil && validSelectionID(journal.ID) {
		return fmt.Errorf("%w (job %s)", ErrSelectionInProgress, journal.ID)
	}
	return ErrSelectionInProgress
}

// ReadSelection is for inspection. A runtime must retain OpenSelection's lease.
func ReadSelection(path string) (Selection, error) {
	if err := selectionJournalError(path); err != nil {
		return Selection{}, err
	}
	s, err := readSelected(path)
	if err != nil {
		return Selection{}, err
	}
	if err := selectionJournalError(path); err != nil {
		return Selection{}, err
	}
	return s, nil
}

// OpenSelection returns a shared local lease held until service shutdown. On
// any error, including an absent record, no lease remains to deadlock adoption.
func OpenSelection(path string) (Selection, io.Closer, error) {
	lease, err := acquireSelectionLease(path, false)
	if err != nil {
		return Selection{}, nil, err
	}
	s, err := ReadSelection(path)
	if err != nil {
		lease.Close()
		return Selection{}, nil, err
	}
	return s, lease, nil
}

// AdoptSelection follows caller validation of an existing or newly initialized
// installation. It never silently changes an existing selection's targets.
func AdoptSelection(path string, validated Selection) (Selection, error) {
	if err := validated.validate(); err != nil {
		return Selection{}, err
	}
	if SelectionPath(validated.StateDir) != path {
		return Selection{}, errors.New("adoption state directory differs from discovery path")
	}
	if current, lease, err := OpenSelection(path); err == nil {
		defer lease.Close()
		if !sameSelectionTargets(current, validated) {
			return Selection{}, ErrSelectionChanged
		}
		return current, nil
	} else if !errors.Is(err, ErrSelectionMissing) {
		return Selection{}, err
	}
	if err := os.MkdirAll(validated.StateDir, 0700); err != nil {
		return Selection{}, err
	}
	u, err := BeginSelectionUpdate(path, "")
	if err != nil {
		return Selection{}, err
	}
	defer u.Close()
	if _, err := u.Commit(validated); err != nil {
		return Selection{}, err
	}
	return validated, nil
}

type selectionJournal struct {
	Version int        `json:"version"`
	ID      string     `json:"id"`
	Source  *Selection `json:"source"`
	Next    *Selection `json:"next,omitempty"`
}

// SelectionStatus is safe to inspect before choosing a database service or
// loading credentials. Unrecorded means setup validation is still required.
type SelectionStatus struct {
	Version       int     `json:"version"`
	State         string  `json:"state"`
	Backend       Backend `json:"backend"`
	Generation    string  `json:"generation"`
	JobID         string  `json:"job_id"`
	SourceBackend Backend `json:"source_backend"`
	TargetBackend Backend `json:"target_backend"`
	StateDir      string  `json:"state_dir"`
	Telemetry     *Target `json:"telemetry"`
	Accounts      *Target `json:"accounts"`
}

func (s *SelectionStatus) selectedTargets(selected Selection) {
	s.Backend, s.Generation, s.StateDir = selected.Backend, selected.Generation, selected.StateDir
	s.Telemetry, s.Accounts = &selected.Telemetry, selected.Accounts
}

func InspectSelection(path string) (SelectionStatus, error) {
	var status SelectionStatus
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		status, err = inspectSelection(path)
		if !errors.Is(err, ErrSelectionInProgress) && !errors.Is(err, ErrSelectionChanged) {
			return status, err
		}
	}
	if errors.Is(err, ErrSelectionInProgress) {
		status.State = "pending"
		return status, nil
	}
	return status, err
}

func inspectSelection(path string) (SelectionStatus, error) {
	status := SelectionStatus{Version: selectionVersion, State: "corrupt"}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return status, errors.New("selection path must be absolute and normalized")
	}
	status.StateDir = filepath.Dir(path)
	var journal selectionJournal
	err := readSelectionJSON(path+".switch.json", &journal)
	if err == nil {
		if err = journal.validate(path); err != nil {
			return status, err
		}
		u := SelectionUpdate{path: path, journal: journal}
		replaced, err := u.currentState()
		if err != nil {
			return status, err
		}
		status.State, status.JobID = "pending", journal.ID
		if journal.Source != nil {
			status.SourceBackend = journal.Source.Backend
			status.selectedTargets(*journal.Source)
		}
		if journal.Next != nil {
			status.TargetBackend = journal.Next.Backend
			if replaced {
				status.selectedTargets(*journal.Next)
			}
		}
		return status, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return status, err
	}
	s, err := ReadSelection(path)
	if errors.Is(err, ErrSelectionMissing) {
		status.State = "unrecorded"
		return status, nil
	}
	if err != nil {
		return status, err
	}
	status.State = "ready"
	status.selectedTargets(s)
	return status, nil
}

func (j selectionJournal) validate(path string) error {
	if j.Version != 1 || !validSelectionID(j.ID) {
		return errors.New("invalid selection recovery journal")
	}
	for _, s := range []*Selection{j.Source, j.Next} {
		if s == nil {
			continue
		}
		if err := s.validate(); err != nil {
			return err
		}
		if SelectionPath(s.StateDir) != path {
			return errors.New("journal state directory differs from discovery path")
		}
	}
	if j.Source != nil && j.Next != nil && j.Source.Generation == j.Next.Generation {
		return ErrSelectionChanged
	}
	return nil
}

// SelectionUpdate is a single local selection-file transaction, not a database
// converter. Its caller verifies both stores and holds native DB writer fences.
type SelectionUpdate struct {
	path         string
	lease        *selectionLease
	journal      selectionJournal
	afterReplace func() error // fault injection in package tests
}

func (u *SelectionUpdate) ID() string { return u.journal.ID }
func (u *SelectionUpdate) Current() Selection {
	if u.journal.Source == nil {
		return Selection{}
	}
	return cloneSelection(*u.journal.Source)
}

func (u *SelectionUpdate) Target() Selection {
	if u.journal.Next == nil {
		return Selection{}
	}
	return cloneSelection(*u.journal.Next)
}

// Stage binds the exact unselected destination before any copy begins. It does
// not authorize startup or claim data verification; only Commit selects it.
func (u *SelectionUpdate) Stage(next Selection) error {
	if u.lease == nil {
		return errors.New("selection update is closed")
	}
	if _, err := u.currentState(); err != nil {
		return err
	}
	if err := next.validate(); err != nil {
		return err
	}
	if SelectionPath(next.StateDir) != u.path || (u.journal.Source != nil && next.StateDir != u.journal.Source.StateDir) {
		return errors.New("backend switching cannot relocate the installation state directory")
	}
	if u.journal.Source != nil && next.Generation == u.journal.Source.Generation {
		return errors.New("selection update requires a new generation")
	}
	if u.journal.Next != nil {
		if !reflect.DeepEqual(next, *u.journal.Next) {
			return ErrSelectionChanged
		}
		return nil
	}
	staged := u.journal
	next = cloneSelection(next)
	staged.Next = &next
	if _, err := writeSelectionJSON(u.path+".switch.json", staged); err != nil {
		return err
	}
	u.journal = staged
	return nil
}
func (u *SelectionUpdate) Close() error {
	if u.lease == nil {
		return nil
	}
	err := u.lease.Close()
	u.lease = nil
	return err
}

func BeginSelectionUpdate(path, expectedGeneration string) (*SelectionUpdate, error) {
	lease, err := acquireSelectionLease(path, true)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*SelectionUpdate, error) { lease.Close(); return nil, err }
	if err := selectionJournalError(path); err != nil {
		return fail(err)
	}
	current, err := readSelected(path)
	if err != nil && !errors.Is(err, ErrSelectionMissing) {
		return fail(err)
	}
	if (err == nil && current.Generation != expectedGeneration) || (err != nil && expectedGeneration != "") {
		return fail(ErrSelectionChanged)
	}
	id, err := newSelectionID()
	if err != nil {
		return fail(err)
	}
	u := &SelectionUpdate{path: path, lease: lease, journal: selectionJournal{Version: 1, ID: id}}
	if current.Generation != "" {
		u.journal.Source = &current
	}
	if _, err = writeSelectionJSON(path+".switch.json", u.journal); err != nil {
		return fail(err)
	}
	return u, nil
}

func ResumeSelectionUpdate(path, jobID string) (*SelectionUpdate, error) {
	lease, err := acquireSelectionLease(path, true)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*SelectionUpdate, error) { lease.Close(); return nil, err }
	var journal selectionJournal
	if err := readSelectionJSON(path+".switch.json", &journal); err != nil {
		return fail(err)
	}
	if journal.ID != jobID {
		return fail(errors.New("selection recovery requires the exact recorded job"))
	}
	if err := journal.validate(path); err != nil {
		return fail(err)
	}
	u := &SelectionUpdate{path: path, lease: lease, journal: journal}
	if _, err := u.currentState(); err != nil {
		return fail(err)
	}
	return u, nil
}

func (u *SelectionUpdate) currentState() (bool, error) {
	current, err := readSelected(u.path)
	if errors.Is(err, ErrSelectionMissing) && u.journal.Source == nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if u.journal.Next != nil && reflect.DeepEqual(current, *u.journal.Next) {
		return true, nil
	}
	if u.journal.Source != nil && reflect.DeepEqual(current, *u.journal.Source) {
		return false, nil
	}
	return false, ErrSelectionChanged
}

// Commit is called after complete source/target verification. A true result
// with an error means replacement occurred but recovery/cleanup is unfinished.
// Inspect status after cleanup errors; Abort never restores the old record.
func (u *SelectionUpdate) Commit(next Selection) (bool, error) {
	if u.lease == nil {
		return false, errors.New("selection update is closed")
	}
	replaced, err := u.currentState()
	if err != nil {
		return false, err
	}
	if err := u.Stage(next); err != nil {
		return replaced, err
	}
	if !replaced {
		replaced, err = writeSelectionJSON(u.path, next)
		if err != nil {
			// Native replacement errors can be ambiguous. Classify from the actual
			// record, keeping the journal regardless; never claim an old selection.
			if actual, readErr := readSelected(u.path); readErr == nil && reflect.DeepEqual(actual, next) {
				replaced = true
			}
			return replaced, err
		}
		if u.afterReplace != nil {
			if err := u.afterReplace(); err != nil {
				return true, err
			}
		}
	}
	if err := os.Remove(u.path + ".switch.json"); err != nil {
		return true, err
	}
	return true, syncSelectionDirectory(filepath.Dir(u.path))
}

func (u *SelectionUpdate) Abort() error {
	if u.lease == nil {
		return errors.New("selection update is closed")
	}
	replaced, err := u.currentState()
	if err != nil {
		return err
	}
	if replaced {
		return errors.New("selected generation already changed; resume verified completion instead of aborting")
	}
	if err := os.Remove(u.path + ".switch.json"); err != nil {
		return err
	}
	return syncSelectionDirectory(filepath.Dir(u.path))
}

func writeSelectionJSON(path string, value any) (replaced bool, err error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return false, err
	}
	if len(data) > maxSelectionBytes {
		return false, errors.New("selection metadata exceeds its bound")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".storage-selection-*")
	if err != nil {
		return false, err
	}
	temp := f.Name()
	defer func() { f.Close(); os.Remove(temp) }()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if err = f.Close(); err != nil {
		return false, err
	}
	if err = replaceSelectionFile(temp, path); err != nil {
		return false, err
	}
	return true, syncSelectionDirectory(filepath.Dir(path))
}
