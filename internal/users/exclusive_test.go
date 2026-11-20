package users

import (
	"context"
	"testing"
)

func TestTryExclusiveSQLiteAlwaysOK(t *testing.T) {
	st, _ := newSQLiteTestStore(t)
	ctx := context.Background()
	r1, ok1, err1 := st.TryExclusive(ctx, "prune")
	r2, ok2, err2 := st.TryExclusive(ctx, "prune")
	if err1 != nil || err2 != nil || !ok1 || !ok2 {
		t.Fatalf("TryExclusive = %v/%v, %v/%v; want ok twice", ok1, err1, ok2, err2)
	}
	r1()
	r1()
	r2()
}

func TestTryExclusiveRejectsEmptyName(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newTestStore storeFactory) {
		st, _ := newTestStore(t)
		if _, ok, err := st.TryExclusive(context.Background(), ""); err == nil || ok {
			t.Fatalf("empty name: ok=%v err=%v, want an error", ok, err)
		}
	})
}

func TestPostgresTryExclusive(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	ctx := context.Background()
	// A name unique to this test run, so a concurrent run elsewhere on the
	// cluster cannot hold it.
	name := "test-" + newPostgresTestSchema(t)

	release, ok, err := a.TryExclusive(ctx, name)
	if err != nil || !ok {
		t.Fatalf("first TryExclusive = %v, %v; want ok", ok, err)
	}
	if _, ok, err := b.TryExclusive(ctx, name); err != nil || ok {
		t.Fatalf("second replica while held = %v, %v; want ok=false", ok, err)
	}
	// Session locks are re-entrant per session, not per store: the same
	// store asking again uses another connection and is refused too.
	if _, ok, err := a.TryExclusive(ctx, name); err != nil || ok {
		t.Fatalf("same store while held = %v, %v; want ok=false", ok, err)
	}
	// Another name is independent.
	other, ok, err := b.TryExclusive(ctx, name+"-other")
	if err != nil || !ok {
		t.Fatalf("other name = %v, %v; want ok", ok, err)
	}
	other()

	release()
	release() // idempotent
	release2, ok, err := b.TryExclusive(ctx, name)
	if err != nil || !ok {
		t.Fatalf("after release = %v, %v; want ok", ok, err)
	}
	release2()

	// Released connections go back to the pool without the lock: every
	// pooled connection of a can take it again.
	for i := 0; i < pgMaxOpenConns+1; i++ {
		r, ok, err := a.TryExclusive(ctx, name)
		if err != nil || !ok {
			t.Fatalf("round %d = %v, %v; want ok", i, ok, err)
		}
		r()
	}
}

// A held lock keeps one pooled connection; the refused attempts give theirs
// back, so the pool is not drained by replicas polling a busy job.
func TestPostgresTryExclusiveRefusalFreesConnection(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	ctx := context.Background()
	name := "test-" + newPostgresTestSchema(t)
	release, ok, err := a.TryExclusive(ctx, name)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	defer release()
	for i := 0; i < pgMaxOpenConns*2; i++ {
		if _, ok, err := b.TryExclusive(ctx, name); err != nil || ok {
			t.Fatalf("attempt %d = %v, %v", i, ok, err)
		}
	}
	if n := b.db.Stats().InUse; n != 0 {
		t.Fatalf("connections in use after refusals = %d, want 0", n)
	}
}

func TestExclusiveLockKeyStable(t *testing.T) {
	if exclusiveLockKey("a") == exclusiveLockKey("b") {
		t.Fatal("distinct names share a key")
	}
	if exclusiveLockKey("a") != exclusiveLockKey("a") {
		t.Fatal("key not deterministic")
	}
	if exclusiveLockKey("a") == pgMigrateLockKey {
		t.Fatal("collides with the migration lock")
	}
}
