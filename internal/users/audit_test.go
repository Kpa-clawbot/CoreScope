package users

import "testing"

func TestAuditWriteAndRead(t *testing.T) {
	st, clk := newTestStore(t)
	admin := mustCreate(t, st, "a@example.org", "Adm")
	u := mustCreate(t, st, "u@example.org", "Usr")
	aid, uid := admin.ID, u.ID
	if err := st.Audit(&aid, "user.disable", &uid, map[string]string{"reason": "spam"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Audit(nil, "user.register", &uid, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := st.AuditFor(u.ID, 50)
	if err != nil || len(entries) != 2 {
		t.Fatalf("AuditFor = %d, %v", len(entries), err)
	}
	var disable *AuditEntry
	for i := range entries {
		if entries[i].Action == "user.disable" {
			disable = &entries[i]
		}
	}
	if disable == nil || disable.ActorUserID == nil || *disable.ActorUserID != aid ||
		disable.Detail["reason"] != "spam" || !disable.At.Equal(clk.Now()) {
		t.Fatalf("disable entry = %+v", disable)
	}
	// Entries survive the target's deletion.
	st.Delete(u.ID)
	if entries, _ := st.AuditFor(uid, 50); len(entries) != 2 {
		t.Fatalf("audit rows lost on delete: %d", len(entries))
	}
}
