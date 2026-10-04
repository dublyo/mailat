package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log"
	"time"

	"github.com/dublyo/mailat/api/internal/config"
)

const dmarcBackfillName = "aggregate-v1"
const dmarcBackfillBatchSize = 25

type dmarcHistoricalMessage struct {
	ID, OrgID, IdentityID, UserID int64
	UUID, Bucket, Key             string
	UpdatedAt                     time.Time
	Verdicts                      dmarcVerdicts
}

// RunDMARCReportBackfill owns one bounded maintenance loop and stops with the
// server. A durable migration-time cutoff prevents later manual Inbox restores
// from ever becoming historical scan candidates.
func RunDMARCReportBackfill(ctx context.Context, db *sql.DB, cfg *config.Config) {
	svc, err := NewReceivingService(db, cfg.AWSRegion, cfg.AWSAccessKeyID, cfg.AWSSecretAccessKey, cfg.APIUrl)
	if err != nil {
		log.Print("DMARC history: storage initialization unavailable")
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		work, cancel := context.WithTimeout(ctx, 2*time.Minute)
		done, err := svc.backfillDMARCReportsBatch(work)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Print("DMARC history: batch interrupted; progress retained")
		}
		if done {
			var scanned, moved, skipped, failed int64
			if err := db.QueryRowContext(ctx, `SELECT scanned,moved,skipped,failed FROM dmarc_report_backfills WHERE name=$1`, dmarcBackfillName).Scan(&scanned, &moved, &skipped, &failed); err == nil {
				log.Printf("DMARC history: complete; scanned=%d moved=%d skipped=%d failed=%d", scanned, moved, skipped, failed)
			} else {
				log.Print("DMARC history: one-time scan complete")
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *ReceivingService) backfillDMARCReportsBatch(ctx context.Context) (bool, error) {
	// A session advisory lock serializes API replicas without holding a SQL
	// transaction or mailbox lock during object storage downloads.
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Close()
	var acquired bool
	if err = conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock(hashtextextended(current_schema()||':mailat:dmarc-history:v1',0))`).Scan(&acquired); err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlock, `SELECT pg_advisory_unlock(hashtextextended(current_schema()||':mailat:dmarc-history:v1',0))`); err != nil {
			// Do not return a session holding the lock to the connection pool.
			_ = conn.Raw(func(interface{}) error { return driver.ErrBadConn })
		}
	}()
	var cutoff, last int64
	var started time.Time
	var completed, next sql.NullTime
	err = conn.QueryRowContext(ctx, `SELECT cutoff_id,last_id,started_at,completed_at,next_attempt_at FROM dmarc_report_backfills WHERE name=$1`, dmarcBackfillName).Scan(&cutoff, &last, &started, &completed, &next)
	if err != nil {
		return false, err
	}
	if completed.Valid {
		return true, nil
	}
	if next.Valid && time.Now().Before(next.Time) {
		return false, nil
	}
	for i := 0; i < dmarcBackfillBatchSize; i++ {
		var mail dmarcHistoricalMessage
		// Missing verdicts, user opt-outs and mail modified since deployment are not
		// candidates. A later scan will never revisit them after progress advances.
		err = conn.QueryRowContext(ctx, `SELECT e.id,e.uuid,e.org_id,e.identity_id,i.user_id,COALESCE(e.raw_s3_bucket,''),COALESCE(e.raw_s3_key,''),e.updated_at,
   COALESCE(e.dmarc_verdict,''),COALESCE(e.spf_verdict,''),COALESCE(e.dkim_verdict,''),COALESCE(e.spam_verdict,''),COALESCE(e.virus_verdict,'')
   FROM received_emails e JOIN identities i ON i.id=e.identity_id JOIN users u ON u.id=i.user_id AND u.org_id=e.org_id
   LEFT JOIN user_settings prefs ON prefs.user_id=u.id
   WHERE e.id>$1 AND e.id<=$2 AND e.updated_at<=$3 AND e.direction='inbound' AND e.folder='inbox'
    AND NOT e.is_archived AND NOT e.is_trashed AND NOT e.is_spam AND e.has_attachments AND u.status='active'
    AND COALESCE(prefs.auto_organize_dmarc_reports,true) AND e.dmarc_verdict='PASS'
    AND (e.spf_verdict='PASS' OR e.dkim_verdict='PASS')
    AND COALESCE(e.spam_verdict,'')!='FAIL' AND COALESCE(e.virus_verdict,'')!='FAIL'
   ORDER BY e.id LIMIT 1`, last, cutoff, started).Scan(&mail.ID, &mail.UUID, &mail.OrgID, &mail.IdentityID, &mail.UserID, &mail.Bucket, &mail.Key, &mail.UpdatedAt, &mail.Verdicts.DMARC, &mail.Verdicts.SPF, &mail.Verdicts.DKIM, &mail.Verdicts.Spam, &mail.Verdicts.Virus)
		if err == sql.ErrNoRows {
			_, err = conn.ExecContext(ctx, `UPDATE dmarc_report_backfills SET last_id=cutoff_id,completed_at=clock_timestamp(),next_attempt_at=NULL WHERE name=$1`, dmarcBackfillName)
			return err == nil, err
		}
		if err != nil {
			return false, err
		}
		var domains []string
		storageUnavailable := mail.Bucket == "" || mail.Key == ""
		if !storageUnavailable {
			download, cancel := context.WithTimeout(ctx, 30*time.Second)
			raw, readErr := s.storage.GetEmailFromS3(download, mail.Bucket, mail.Key)
			cancel()
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			if readErr != nil {
				storageUnavailable = true
			} else if parsed, parseErr := parseIncomingMIME(raw); parseErr == nil {
				domains, _ = dmarcReportDomains(parsed, mail.Verdicts)
			}
		}
		if storageUnavailable {
			// Three bounded retries survive restarts. An unavailable historical object
			// must neither hide the message nor block all later users indefinitely.
			var attempts int
			err = conn.QueryRowContext(ctx, `UPDATE dmarc_report_backfills SET retry_count=CASE WHEN retry_message_id=$2 THEN retry_count+1 ELSE 1 END,retry_message_id=$2,next_attempt_at=clock_timestamp()+interval '30 seconds',last_error='historical_source_unavailable' WHERE name=$1 RETURNING retry_count`, dmarcBackfillName, mail.ID).Scan(&attempts)
			if err != nil {
				return false, err
			}
			if attempts < 3 {
				return false, nil
			}
			log.Printf("DMARC history: message %d retained; source unavailable after 3 attempts", mail.ID)
			if err = s.finishDMARCBackfillMessage(ctx, conn, mail, false, true); err != nil {
				return false, err
			}
		} else {
			matched, ownershipErr := ownedDMARCReport(ctx, conn, mail.OrgID, domains)
			if ownershipErr != nil {
				return false, ownershipErr // Retry without advancing durable progress.
			}
			if err = s.finishDMARCBackfillMessage(ctx, conn, mail, matched, false); err != nil {
				return false, err
			}
		}
		last = mail.ID
	}
	log.Printf("DMARC history: batch progressed through message %d", last)
	return false, nil
}

func (s *ReceivingService) finishDMARCBackfillMessage(ctx context.Context, conn *sql.Conn, mail dmarcHistoricalMessage, matched, failed bool) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// This is the same user lock used by settings and every mailbox mutation.
	// Opt-out, read/archive and Inbox restores therefore win when they commit
	// before this final version check, even if classification was in flight.
	if err = lockMailboxLabels(ctx, tx, mail.UserID); err != nil {
		return err
	}
	moved := int64(0)
	if matched {
		enabled, err := autoOrganizeDMARC(ctx, tx, mail.UserID)
		if err != nil {
			return err
		}
		if enabled {
			result, err := tx.ExecContext(ctx, `UPDATE received_emails e SET folder=$1,is_archived=false,is_trashed=false,is_spam=false,updated_at=clock_timestamp()
    FROM identities i JOIN users u ON u.id=i.user_id
    WHERE e.id=$2 AND e.uuid=$3 AND e.identity_id=i.id AND e.identity_id=$4 AND i.user_id=$5 AND e.org_id=$6 AND u.org_id=$6 AND u.status='active'
     AND e.updated_at=$7 AND e.folder='inbox' AND e.direction='inbound' AND NOT e.is_archived AND NOT e.is_trashed AND NOT e.is_spam`, DMARCReportsFolder, mail.ID, mail.UUID, mail.IdentityID, mail.UserID, mail.OrgID, mail.UpdatedAt)
			if err != nil {
				return err
			}
			moved, err = result.RowsAffected()
			if err != nil {
				return err
			}
		}
	}
	failedCount := 0
	if failed {
		failedCount = 1
	}
	result, err := tx.ExecContext(ctx, `UPDATE dmarc_report_backfills SET last_id=$2,scanned=scanned+1,moved=moved+$3,skipped=skipped+(1-$3),failed=failed+$4,retry_message_id=NULL,retry_count=0,next_attempt_at=NULL WHERE name=$1 AND last_id<$2`, dmarcBackfillName, mail.ID, moved, failedCount)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("historical progress changed")
	}
	// The mailbox trigger records a normal update; no email.received event or
	// duplicate automation is emitted for historical organization.
	return tx.Commit()
}
