package users

import (
	"database/sql"
	"errors"
	"time"
)

const pushBindingLimit = 16

var ErrPushBindingLimit = errors.New("users: push device limit reached")

// PushBinding describes an installation's staged or active link to a CoreScope account.
type PushBinding struct {
	AnalyzerID, InstallationID string
	UserID                     int64
	BindingID, ProofHash       string
	SubscriptionID             string
	ExpiresAt                  time.Time
}

// PushBindingsFor lists userID's active device links, oldest expiry first.
func (s *Store) PushBindingsFor(userID int64) ([]PushBinding, error) {
	rows, err := s.db.Query(`SELECT analyzer_id, installation_id, user_id, binding_id, proof_hash, subscription_id, expires_at
		FROM notification_push_bindings WHERE user_id = ? ORDER BY expires_at, analyzer_id, installation_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushBinding
	for rows.Next() {
		var b PushBinding
		var exp int64
		if err := rows.Scan(&b.AnalyzerID, &b.InstallationID, &b.UserID, &b.BindingID, &b.ProofHash, &b.SubscriptionID, &exp); err != nil {
			return nil, err
		}
		b.ExpiresAt = fromUnix(exp)
		out = append(out, b)
	}
	return out, rows.Err()
}

// StagePushBinding reserves an idempotency key before CoreScope calls the relay.
// A pending cross-account link cannot change the active binding.
func (s *Store) StagePushBinding(analyzerID, installationID string, userID int64, bindingID, proofHash string) (PushBinding, error) {
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return PushBinding{}, err
	}
	defer tx.Rollback()
	if err := requireActivePushUser(tx, userID); err != nil {
		return PushBinding{}, err
	}
	if _, err := tx.Exec(`DELETE FROM notification_push_intents WHERE expires_at <= ?`, unix(now)); err != nil {
		return PushBinding{}, err
	}
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM (
		SELECT installation_id FROM notification_push_bindings
			WHERE user_id = ? AND analyzer_id = ? AND installation_id <> ? AND expires_at > ?
		UNION
		SELECT installation_id FROM notification_push_intents
			WHERE user_id = ? AND analyzer_id = ? AND installation_id <> ?
	)`, userID, analyzerID, installationID, unix(now), userID, analyzerID, installationID).Scan(&count); err != nil {
		return PushBinding{}, err
	}
	if count >= pushBindingLimit {
		return PushBinding{}, ErrPushBindingLimit
	}

	binding := PushBinding{AnalyzerID: analyzerID, InstallationID: installationID, UserID: userID,
		BindingID: bindingID, ProofHash: proofHash}
	var activeID, activeProof string
	var activeExpiry int64
	err = tx.QueryRow(`SELECT binding_id, proof_hash, subscription_id, expires_at FROM notification_push_bindings
		WHERE analyzer_id = ? AND installation_id = ? AND user_id = ? AND expires_at > ?`,
		analyzerID, installationID, userID, unix(now)).Scan(&activeID, &activeProof, &binding.SubscriptionID, &activeExpiry)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return PushBinding{}, err
	}
	if err == nil {
		binding.BindingID, binding.ExpiresAt = activeID, fromUnix(activeExpiry)
		if activeProof == proofHash {
			return binding, tx.Commit()
		}
	} else {
		var pendingID string
		err := tx.QueryRow(`SELECT binding_id FROM notification_push_intents
			WHERE analyzer_id = ? AND installation_id = ? AND user_id = ?`, analyzerID, installationID, userID).Scan(&pendingID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return PushBinding{}, err
		}
		if err == nil {
			binding.BindingID = pendingID
		}
	}

	if _, err := tx.Exec(`INSERT INTO notification_push_intents (analyzer_id, installation_id, user_id, binding_id, proof_hash, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(analyzer_id, installation_id, user_id) DO UPDATE
		SET binding_id = excluded.binding_id, proof_hash = excluded.proof_hash, expires_at = excluded.expires_at`,
		analyzerID, installationID, userID, binding.BindingID, proofHash, unix(now.Add(5*time.Minute))); err != nil {
		return PushBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return PushBinding{}, err
	}
	return binding, nil
}

func requireActivePushUser(tx *sql.Tx, userID int64) error {
	var status Status
	err := tx.QueryRow(`SELECT status FROM users WHERE id = ?`, userID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) || err == nil && status != StatusActive {
		return ErrNotFound
	}
	return err
}

// ActivatePushBinding installs the relay-confirmed subscription and revokes
// any replaced subscription in the same transaction.
func (s *Store) ActivatePushBinding(binding PushBinding, subscriptionID string, expiresAt time.Time) (PushBinding, error) {
	now := unix(s.now())
	if subscriptionID == "" || unix(expiresAt) <= now {
		return PushBinding{}, ErrTokenExpired
	}
	tx, err := s.db.Begin()
	if err != nil {
		return PushBinding{}, err
	}
	defer tx.Rollback()
	if err := requireActivePushUser(tx, binding.UserID); err != nil {
		return PushBinding{}, err
	}
	res, err := tx.Exec(`DELETE FROM notification_push_intents
		WHERE analyzer_id = ? AND installation_id = ? AND user_id = ?
			AND binding_id = ? AND proof_hash = ? AND expires_at > ?`,
		binding.AnalyzerID, binding.InstallationID, binding.UserID, binding.BindingID, binding.ProofHash, now)
	if err != nil {
		return PushBinding{}, err
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return PushBinding{}, err
	}
	if deleted == 0 {
		var expiry int64
		err := tx.QueryRow(`SELECT expires_at FROM notification_push_bindings WHERE analyzer_id = ? AND installation_id = ?
			AND user_id = ? AND binding_id = ? AND proof_hash = ? AND subscription_id = ? AND expires_at > ?`,
			binding.AnalyzerID, binding.InstallationID, binding.UserID, binding.BindingID, binding.ProofHash, subscriptionID, now).Scan(&expiry)
		if errors.Is(err, sql.ErrNoRows) {
			return PushBinding{}, ErrNotFound
		}
		if err != nil {
			return PushBinding{}, err
		}
		binding.SubscriptionID, binding.ExpiresAt = subscriptionID, fromUnix(expiry)
		return binding, tx.Commit()
	}
	if _, err := tx.Exec(`DELETE FROM notification_push_bindings
		WHERE analyzer_id = ? AND installation_id = ? AND subscription_id <> ?`,
		binding.AnalyzerID, binding.InstallationID, subscriptionID); err != nil {
		return PushBinding{}, err
	}
	res, err = tx.Exec(`INSERT INTO notification_push_bindings
		(analyzer_id, installation_id, user_id, binding_id, proof_hash, subscription_id, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(analyzer_id, installation_id) DO UPDATE SET proof_hash = excluded.proof_hash, expires_at = excluded.expires_at
		WHERE user_id = excluded.user_id AND binding_id = excluded.binding_id AND subscription_id = excluded.subscription_id`,
		binding.AnalyzerID, binding.InstallationID, binding.UserID, binding.BindingID, binding.ProofHash, subscriptionID, unix(expiresAt))
	if err := expectOne(res, err); err != nil {
		return PushBinding{}, err
	}
	if _, err := tx.Exec(`DELETE FROM notification_push_intents WHERE analyzer_id = ? AND installation_id = ?`,
		binding.AnalyzerID, binding.InstallationID); err != nil {
		return PushBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return PushBinding{}, err
	}
	binding.SubscriptionID, binding.ExpiresAt = subscriptionID, fromUnix(unix(expiresAt))
	return binding, nil
}

// CancelPushBinding removes the specific failed attempt without changing an active link.
func (s *Store) CancelPushBinding(binding PushBinding) error {
	_, err := s.db.Exec(`DELETE FROM notification_push_intents
		WHERE analyzer_id = ? AND installation_id = ? AND user_id = ? AND binding_id = ? AND proof_hash = ?`,
		binding.AnalyzerID, binding.InstallationID, binding.UserID, binding.BindingID, binding.ProofHash)
	return err
}

// RevokePushBinding disables this account's binding and queues relay revocation.
// The trigger also handles active bindings removed by cascading account deletion.
func (s *Store) RevokePushBinding(analyzerID, installationID string, userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM notification_push_intents WHERE analyzer_id = ? AND installation_id = ? AND user_id = ?`,
		analyzerID, installationID, userID); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notification_push_bindings WHERE analyzer_id = ? AND installation_id = ? AND user_id = ?`,
		analyzerID, installationID, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// PushEvent is an evaluated watched-node transition ready for the push outbox.
type PushEvent struct {
	UserID                int64
	EventID, Event, Kind  string
	NodePubkey, NodeName  string
	OccurredAt, ExpiresAt time.Time
}

// PushJob is a queued relay action. Only the notifier dispatches jobs.
type PushJob struct {
	ID                                  int64
	Operation, SubscriptionID           string
	EventID, Kind, NodePubkey, NodeName string
	OccurredAt, ExpiresAt               time.Time
	Attempts                            int
}

// ApplyNotifyChanges saves evaluator state and queues push deliveries atomically.
// NotifyOutbox is what the delivery destinations queue with one
// evaluation: rows written in the same transaction as its states, so an
// alert is never recorded without its deliveries or the other way round.
// Each destination fills its own fields.
type NotifyOutbox struct {
	// PushAnalyzerID and PushEvents: jobs for the push relay the analyzer
	// is enrolled with, fanned out to the users' device links.
	PushAnalyzerID string
	PushEvents     []PushEvent
}

// ApplyNotifyChanges deletes drop, upserts states and queues out in one
// transaction.
func (s *Store) ApplyNotifyChanges(drop []NotifyKey, states []NotifyState, out NotifyOutbox) error {
	if len(drop) == 0 && len(states) == 0 && len(out.PushEvents) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteNotifyStates(tx, drop); err != nil {
		return err
	}
	if err := writeNotifyStates(tx, states); err != nil {
		return err
	}
	if len(out.PushEvents) > 0 {
		if err := queuePushEvents(tx, out.PushAnalyzerID, s.now(), out.PushEvents); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Push only supports the two watch event types which also have default
// preferences. The email-enabled flag is intentionally absent.
const pushWatchAllowed = `u.status = 'active'
	AND EXISTS (SELECT 1 FROM notification_watches w
		WHERE w.user_id = b.user_id AND w.pubkey = e.node_pubkey AND w.created_at <= e.occurred_at)
	AND (p.user_id IS NULL OR instr(',' || p.events || ',', ',' || e.event || ',') > 0)`

func queuePushEvents(tx *sql.Tx, analyzerID string, now time.Time, events []PushEvent) error {
	stmt, err := tx.Prepare(`WITH e(user_id,event_id,event,kind,node_pubkey,node_name,occurred_at,expires_at) AS (VALUES (?,?,?,?,?,?,?,?))
		INSERT INTO notification_push_jobs
			(analyzer_id,operation,subscription_id,user_id,event_id,event,kind,node_pubkey,node_name,occurred_at,expires_at)
		SELECT b.analyzer_id,'deliver',b.subscription_id,e.user_id,e.event_id,e.event,e.kind,e.node_pubkey,e.node_name,e.occurred_at,e.expires_at
		FROM e
		JOIN notification_push_bindings b ON b.user_id = e.user_id AND b.analyzer_id = ?
		JOIN users u ON u.id = b.user_id
		LEFT JOIN notification_prefs p ON p.user_id = b.user_id
		WHERE b.expires_at > ? AND e.expires_at > ? AND e.event IN ('node.offline','node.battery') AND ` + pushWatchAllowed + `
		ON CONFLICT (analyzer_id, subscription_id, event_id) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, e := range events {
		if _, err := stmt.Exec(e.UserID, e.EventID, e.Event, e.Kind, e.NodePubkey, e.NodeName, unix(e.OccurredAt), unix(e.ExpiresAt),
			analyzerID, unix(now), unix(now)); err != nil {
			return err
		}
	}
	return nil
}

// PushPruneCounts reports how many existing rows startup cleanup removed.
type PushPruneCounts struct {
	Bindings, Intents, Jobs int64
}

// PrunePushState removes local state for retired analyzer enrollments.
// An empty keepAnalyzerID removes all push state.
func (s *Store) PrunePushState(keepAnalyzerID string) (PushPruneCounts, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return PushPruneCounts{}, err
	}
	defer tx.Rollback()

	var counts PushPruneCounts
	const predicate = ` WHERE ? = '' OR analyzer_id <> ?`
	const deleteJobs = `DELETE FROM notification_push_jobs` + predicate
	for _, step := range []struct {
		query string
		count *int64
	}{
		// Count existing jobs before the binding trigger deletes deliveries.
		{deleteJobs, &counts.Jobs},
		{`DELETE FROM notification_push_intents` + predicate, &counts.Intents},
		{`DELETE FROM notification_push_bindings` + predicate, &counts.Bindings},
		// Discard revocations queued by the binding trigger for retired analyzers.
		{deleteJobs, nil},
	} {
		result, err := tx.Exec(step.query, keepAnalyzerID, keepAnalyzerID)
		if err != nil {
			return PushPruneCounts{}, err
		}
		if step.count != nil {
			*step.count, err = result.RowsAffected()
			if err != nil {
				return PushPruneCounts{}, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return PushPruneCounts{}, err
	}
	return counts, nil
}

// DropPushDeliveries discards queued alerts and preserves subscription revokes.
func (s *Store) DropPushDeliveries(analyzerID string) error {
	_, err := s.db.Exec(`DELETE FROM notification_push_jobs WHERE analyzer_id = ? AND operation = 'deliver'`, analyzerID)
	return err
}

// DuePushJobs returns a bounded batch and prunes expired work, including jobs
// for a previous analyzer enrollment that no configured dispatcher can send.
func (s *Store) DuePushJobs(analyzerID string, now time.Time, limit int) ([]PushJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	if err := s.prunePushWork(now); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`SELECT id, operation, subscription_id, event_id, kind, node_pubkey, node_name, occurred_at, expires_at, attempts
		FROM notification_push_jobs WHERE analyzer_id = ? AND next_at <= ?
		ORDER BY next_at, id LIMIT ?`, analyzerID, unix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []PushJob
	for rows.Next() {
		var job PushJob
		var occurredAt, expiresAt int64
		if err := rows.Scan(&job.ID, &job.Operation, &job.SubscriptionID, &job.EventID, &job.Kind, &job.NodePubkey, &job.NodeName,
			&occurredAt, &expiresAt, &job.Attempts); err != nil {
			return nil, err
		}
		job.OccurredAt, job.ExpiresAt = fromUnix(occurredAt), fromUnix(expiresAt)
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) prunePushWork(now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`DELETE FROM notification_push_intents WHERE expires_at <= ?`,
		`DELETE FROM notification_push_bindings WHERE expires_at <= ?`,
		`DELETE FROM notification_push_jobs WHERE expires_at <= ?`,
	} {
		if _, err := tx.Exec(q, unix(now)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PushJobEligible rechecks the durable job and live account, watch and binding
// immediately before the dispatcher sends. Revocation survives account deletion.
func (s *Store) PushJobEligible(job PushJob, now time.Time) (bool, error) {
	var eligible bool
	err := s.db.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM notification_push_jobs e
		WHERE e.id = ? AND e.expires_at > ? AND (e.operation = 'revoke' OR EXISTS (
			SELECT 1 FROM notification_push_bindings b
			JOIN users u ON u.id = b.user_id
			LEFT JOIN notification_prefs p ON p.user_id = b.user_id
			WHERE b.analyzer_id = e.analyzer_id AND b.subscription_id = e.subscription_id AND b.user_id = e.user_id
				AND b.expires_at > ? AND `+pushWatchAllowed+`
		))
	)`, job.ID, unix(now), unix(now)).Scan(&eligible)
	return eligible, err
}

func (s *Store) CompletePushJob(id int64) error {
	_, err := s.db.Exec(`DELETE FROM notification_push_jobs WHERE id = ?`, id)
	return err
}

func (s *Store) RetryPushJob(id int64, next time.Time) error {
	_, err := s.db.Exec(`UPDATE notification_push_jobs SET next_at = ?, attempts = attempts + 1 WHERE id = ?`, unix(next), id)
	return err
}
