package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"github.com/dublyo/mailat/api/internal/model"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"time"
)

func (s *AuthService) issueSession(ctx context.Context, user *model.User, version int64, verifiedFactor bool) (string, error) {
	token, err := s.generateToken(user)
	if err != nil {
		return "", err
	}
	var id int64
	// Persist before returning a usable token. The middleware always checks this
	// record, so revocation and account disablement take effect on the next request.
	err = s.db.QueryRowContext(ctx, `INSERT INTO user_sessions(user_id,org_id,token_hash,expires_at) SELECT id,org_id,$2,$3 FROM users WHERE id=$1 AND status='active' AND auth_version=$4 AND totp_enabled=$5 RETURNING id`, user.ID, hashToken(token), s.sessionExpiry(), version, verifiedFactor).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("unable to create session")
	}
	return token, nil
}

// AuthenticateUser is shared by password and OAuth logins; neither can bypass
// the second-factor decision or issue a token for a disabled account.
func (s *AuthService) AuthenticateUser(ctx context.Context, userID int64) (*model.LoginResponse, error) {
	return s.authenticateUser(ctx, userID, nil)
}
func (s *AuthService) authenticateUser(ctx context.Context, userID int64, expectedVersion *int64) (*model.LoginResponse, error) {
	user, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("account unavailable")
	}
	var enabled bool
	var version int64
	if err = s.db.QueryRowContext(ctx, `SELECT totp_enabled,auth_version FROM users WHERE id=$1 AND status='active'`, userID).Scan(&enabled, &version); err != nil {
		return nil, fmt.Errorf("account unavailable")
	}
	if expectedVersion != nil && *expectedVersion != version {
		return nil, fmt.Errorf("credentials changed; sign in again")
	}
	if enabled {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("unable to start verification")
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, userID); err != nil {
			return nil, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM auth_challenges WHERE user_id=$1 AND created_at>now()-interval '15 minutes'`, userID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= 10 {
			return nil, fmt.Errorf("too many verification attempts; try again later")
		}
		b := make([]byte, 32)
		if _, err = rand.Read(b); err != nil {
			return nil, err
		}
		challenge := hex.EncodeToString(b)
		if _, err = tx.ExecContext(ctx, `INSERT INTO auth_challenges(token_hash,user_id,expires_at,auth_version) VALUES($1,$2,now()+interval '5 minutes',$3)`, hashToken(challenge), userID, version); err != nil {
			return nil, err
		}
		// Bounded retention of consumed/expired challenges for this account.
		if _, err = tx.ExecContext(ctx, `DELETE FROM auth_challenges WHERE user_id=$1 AND created_at<now()-interval '1 day'`, userID); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return &model.LoginResponse{RequiresTwoFactor: true, ChallengeToken: challenge}, nil
	}
	token, err := s.issueSession(ctx, user, version, false)
	if err != nil {
		return nil, err
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE users SET last_login_at=now() WHERE id=$1`, userID)
	return &model.LoginResponse{Token: token, User: user}, nil
}

func (s *AuthService) CompleteChallenge(ctx context.Context, challenge, code string) (*model.LoginResponse, error) {
	invalid := fmt.Errorf("invalid or expired verification; sign in again if needed")
	if len(challenge) != 64 || len(code) < 6 || len(code) > 32 {
		return nil, invalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("unable to verify code")
	}
	defer tx.Rollback()
	var userID, challengeVersion int64
	// Serialize challenge attempts, including failure counts. A successful token is
	// single-use; concurrent submissions cannot both establish sessions.
	err = tx.QueryRowContext(ctx, `SELECT user_id,auth_version FROM auth_challenges WHERE token_hash=$1 AND expires_at>now() AND consumed_at IS NULL AND attempts<5 FOR UPDATE`, hashToken(challenge)).Scan(&userID, &challengeVersion)
	if err != nil {
		return nil, invalid
	}
	var secret sql.NullString
	var enabled bool
	var last sql.NullInt64
	var backup []string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT totp_secret,totp_enabled,totp_last_step,backup_codes,auth_version FROM users WHERE id=$1 AND status='active' FOR UPDATE`, userID).Scan(&secret, &enabled, &last, pq.Array(&backup), &version)
	if err != nil || !enabled || version != challengeVersion {
		return nil, invalid
	}
	verified := false
	step := int64(0)
	if secret.Valid {
		for _, offset := range []int64{-1, 0, 1} {
			candidate := time.Now().Unix()/30 + offset
			if (!last.Valid || candidate > last.Int64) && len(code) == 6 && generateTOTP(secret.String, candidate) == code {
				verified = true
				step = candidate
				break
			}
		}
	}
	backupIndex := -1
	if !verified {
		for i, stored := range backup {
			if stored == hashBackupCode(code) {
				verified = true
				backupIndex = i
				break
			}
		}
	}
	if !verified {
		if _, err = tx.ExecContext(ctx, `UPDATE auth_challenges SET attempts=attempts+1 WHERE token_hash=$1`, hashToken(challenge)); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return nil, invalid
	}
	if backupIndex >= 0 {
		backup = append(backup[:backupIndex], backup[backupIndex+1:]...)
		_, err = tx.ExecContext(ctx, `UPDATE users SET backup_codes=$2,last_login_at=now() WHERE id=$1`, userID, pq.Array(backup))
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE users SET totp_last_step=$2,last_login_at=now() WHERE id=$1`, userID, step)
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE auth_challenges SET consumed_at=now() WHERE token_hash=$1`, hashToken(challenge)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	user, err := s.GetUserByID(ctx, userID)
	if err != nil {
		return nil, invalid
	}
	token, err := s.issueSession(ctx, user, version, true)
	if err != nil {
		return nil, err
	}
	return &model.LoginResponse{Token: token, User: user}, nil
}

func (s *AuthService) RevokeToken(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE user_sessions SET active=false,revoked_at=now() WHERE token_hash=$1`, hashToken(token))
	return err
}
func (s *AuthService) StreamToken(ctx context.Context, sessionToken string, user *model.JWTClaims) (string, time.Time, error) {
	var active bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM user_sessions WHERE token_hash=$1 AND user_id=$2 AND active AND expires_at>now())`, hashToken(sessionToken), user.UserID).Scan(&active); err != nil || !active {
		return "", time.Time{}, fmt.Errorf("active user session required")
	}
	expires := time.Now().Add(time.Minute).Truncate(time.Second)
	claims := model.AccessClaims{UserID: user.UserID, OrgID: user.OrgID, Purpose: "stream", SessionHash: hashToken(sessionToken), RegisteredClaims: jwt.RegisteredClaims{Issuer: "mailat", Audience: jwt.ClaimStrings{"mailat-sse"}, ID: uuid.NewString(), IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(expires)}}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(s.cfg.JWTSecret))
	return token, expires, err
}
