package users

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestOpenRejectsCredentialsInErrors(t *testing.T) {
	_, err := Open("postgresql://private-user:private-password@localhost:invalid/accounts")
	if err == nil {
		t.Fatal("invalid database URL accepted")
	}
	for _, secret := range []string{"private-user", "private-password"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("connection error exposed credentials")
		}
	}
}

func TestOpenRefusesSchemaOwner(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := Apply(db); err != nil {
		t.Fatal(err)
	}
	st, err := Open(dsn)
	if st != nil {
		st.Close()
	}
	if err == nil {
		t.Fatal("runtime account connection accepted the migration owner")
	}
}

func TestPostgresSettingsCASAcrossPools(t *testing.T) {
	st, _ := newTestStore(t)
	other, err := Open(st.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	u := mustCreate(t, st, "cas@example.org", "CAS")
	start := make(chan struct{})
	var done sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 20; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			<-start
			s := st
			if i%2 == 1 {
				s = other
			}
			_, err := s.PutSettings(u.ID, SettingsVersion{}, `{"device":true}`)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrSettingsConflict) {
				t.Errorf("settings: %v", err)
			}
		}(i)
	}
	close(start)
	done.Wait()
	if got := won.Load(); got != 1 {
		t.Fatalf("initial settings writers succeeded=%d; want 1", got)
	}
}

func TestPostgresProposalQuotaAcrossPools(t *testing.T) {
	st, _ := newTestStore(t)
	other, err := Open(st.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	u := mustCreate(t, st, "quota@example.org", "Quota")
	start := make(chan struct{})
	var done sync.WaitGroup
	var won atomic.Int32
	for i := 0; i < 20; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			<-start
			s := st
			if i%2 == 1 {
				s = other
			}
			_, err := s.Propose(KindHashtagChannel, strings.Repeat("x", i+1), u.ID, ProposalLimits{MaxPending: 1})
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrProposalPendingLimit) {
				t.Errorf("proposal: %v", err)
			}
		}(i)
	}
	close(start)
	done.Wait()
	if got := won.Load(); got != 1 {
		t.Fatalf("pending proposals accepted=%d; want 1", got)
	}
}

func TestOpenRequiresBootstrap(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	st, err := Open(dsn)
	if st != nil {
		st.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "bootstrap") {
		t.Fatal("runtime Open must report that the PostgreSQL schema needs bootstrap")
	}
}

func TestPostgresSingleUseAndLatestTokenAcrossPools(t *testing.T) {
	st, _ := newTestStore(t)
	other, err := Open(st.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetClock(st.now)
	u := mustCreate(t, st, "token-race@example.org", "Token")
	raw, err := st.IssueToken(u.ID, PurposeReset, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	var won atomic.Int32
	raceStores(st, other, func(s *Store, i int) {
		_, _, err := s.ConsumeToken(raw, PurposeReset)
		if err == nil {
			won.Add(1)
		} else if !errors.Is(err, ErrTokenInvalid) {
			t.Errorf("consume: %v", err)
		}
	})
	if won.Load() != 1 {
		t.Fatalf("token consumed %d times", won.Load())
	}
	tokens := make([]string, 20)
	raceStores(st, other, func(s *Store, i int) {
		var err error
		tokens[i], err = s.IssueToken(u.ID, PurposeReset, time.Hour, "")
		if err != nil {
			t.Errorf("issue: %v", err)
		}
	})
	var valid int
	for _, raw := range tokens {
		if _, err := st.TokenUser(raw, PurposeReset); err == nil {
			valid++
		} else if !errors.Is(err, ErrTokenInvalid) {
			t.Fatal(err)
		}
	}
	if valid != 1 {
		t.Fatalf("live reset tokens=%d; want 1", valid)
	}
}

func TestPostgresWatchAndApprovalQuotasAcrossPools(t *testing.T) {
	st, _ := newTestStore(t)
	other, err := Open(st.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetClock(st.now)
	u := mustCreate(t, st, "watch-race@example.org", "Watch")
	var watched atomic.Int32
	raceStores(st, other, func(s *Store, i int) {
		err := s.AddWatch(u.ID, strings.Repeat("a", i+1), 1)
		if err == nil {
			watched.Add(1)
		} else if !errors.Is(err, ErrWatchLimit) {
			t.Errorf("watch: %v", err)
		}
	})
	if watched.Load() != 1 {
		t.Fatalf("watches=%d; want 1", watched.Load())
	}
	var proposals []*Proposal
	for i := 0; i < 20; i++ {
		p, err := st.Propose(KindHashtagChannel, strings.Repeat("x", i+1), u.ID, ProposalLimits{})
		if err != nil {
			t.Fatal(err)
		}
		proposals = append(proposals, p)
	}
	var approved atomic.Int32
	raceStores(st, other, func(s *Store, i int) {
		_, err := s.Decide(proposals[i].ID, ProposalApprove, u.ID, "", 1)
		if err == nil {
			approved.Add(1)
		} else if !errors.Is(err, ErrProposalApprovedLimit) {
			t.Errorf("approve: %v", err)
		}
	})
	if approved.Load() != 1 {
		t.Fatalf("approved=%d; want 1", approved.Load())
	}
}

func raceStores(first, second *Store, fn func(*Store, int)) {
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s := first
			if i%2 == 1 {
				s = second
			}
			fn(s, i)
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestApplyForImportCannotAppearReady(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	db := testOwner(t, dsn)
	if err := ApplyForImport(db); err != nil {
		t.Fatal(err)
	}
	if err := AssertReady(db); err == nil {
		t.Fatal("unverified import is runtime-ready")
	}
	if err := Apply(db); err == nil {
		t.Fatal("normal bootstrap bypassed import verification")
	}
	if err := ApplyForImport(db); err == nil {
		t.Fatal("restarted bootstrap accepted partial import")
	}
	if _, err := db.Exec(`UPDATE corescope_schema SET ready=true WHERE kind='accounts'`); err != nil {
		t.Fatal(err)
	}
	if err := AssertReady(db); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRuntimeCannotAlterSchemaOrReadiness(t *testing.T) {
	st, _ := newTestStore(t)
	for _, stmt := range []string{`CREATE TABLE forbidden(id int)`, `ALTER TABLE users ADD COLUMN forbidden int`, `UPDATE corescope_schema SET ready=false`, `DELETE FROM schema_version`} {
		if _, err := st.db.Exec(stmt); err == nil {
			t.Fatalf("runtime accepted privileged statement: %s", stmt)
		}
	}
}

func TestNativeAccountTextUsesBinaryCollation(t *testing.T) {
	st, _ := newTestStore(t)
	var nonBinary int
	if err := st.db.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema() AND data_type='text' AND collation_name IS DISTINCT FROM 'C'`).Scan(&nonBinary); err != nil {
		t.Fatal(err)
	}
	if nonBinary != 0 {
		t.Fatalf("%d account text columns inherit locale-dependent collation", nonBinary)
	}
	for _, email := range []string{"a@example.org", "B@example.org", "ä@example.org"} {
		mustCreate(t, st, email, "Case")
	}
	rows, err := st.db.Query(`SELECT email FROM users ORDER BY email`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			t.Fatal(err)
		}
		got = append(got, email)
	}
	if strings.Join(got, ",") != "B@example.org,a@example.org,ä@example.org" {
		t.Fatal("account byte ordering changed")
	}
}

func TestPendingPruneRechecksTokenAfterConcurrentIssue(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "pending-race@example.org", "Pending")
	clk.Advance(8 * 24 * time.Hour)
	other, err := pgutil.Open(st.databaseURL, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockUser(tx, u.ID); err != nil {
		t.Fatal(err)
	}
	type result struct {
		n   int64
		err error
	}
	finished := make(chan result, 1)
	go func() { n, err := st.PruneStalePending(7 * 24 * time.Hour); finished <- result{n, err} }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := other.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE usename=current_user AND wait_event_type='Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prune did not reach the account row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(`INSERT INTO tokens(token_hash,user_id,purpose,expires_at) VALUES('prune-race',$1,'activate',$2)`, u.ID, unix(clk.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got := <-finished
	if got.err != nil || got.n != 0 {
		t.Fatalf("pruned account after new activation token=%d,%v; want 0", got.n, got.err)
	}
	if _, err := st.GetByID(u.ID); err != nil {
		t.Fatal("newly issued activation account was deleted")
	}
}

func TestNotifyStatesSkipConcurrentAccountDeletion(t *testing.T) {
	st, clk := newTestStore(t)
	u := mustCreate(t, st, "delete-race@example.org", "Deleted")
	other, err := pgutil.Open(st.databaseURL, false)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM users WHERE id=$1`, u.ID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		finished <- st.WriteNotifyStates([]NotifyState{nState(u.ID, NotifyNodeOffline, "node", NotifyBad, clk.Now())})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := other.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE usename=current_user AND wait_event_type='Lock'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("notification insert did not reach the account lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatalf("deleted account was not skipped: %v", err)
	}
}
