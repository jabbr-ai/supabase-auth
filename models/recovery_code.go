package models

import (
	"database/sql"
	"github.com/gofrs/uuid"
	"github.com/netlify/gotrue/crypto"
	"github.com/netlify/gotrue/storage"
	"github.com/pkg/errors"
	"time"
)

type RecoveryCode struct {
	ID           uuid.UUID  `json:"id" db:"id"`
	UserID       uuid.UUID  `json:"user_id" db:"user_id"`
	CreatedAt    *time.Time `json:"created_at" db:"created_at"`
	RecoveryCode string     `json:"recovery_code" db:"recovery_code"`
	VerifiedAt   *time.Time `json:"verified_at" db:"verified_at"`
}

func (RecoveryCode) TableName() string {
	tableName := "mfa_recovery_codes"
	return tableName
}

// Returns a new recovery code associated with the user
func NewRecoveryCode(user *User, recoveryCode string, now *time.Time) (*RecoveryCode, error) {
	tokenLength := 10

	id, err := uuid.NewV4()
	if err != nil {
		return nil, errors.Wrap(err, "Error generating unique id")
	}
	code := &RecoveryCode{
		ID:           id,
		UserID:       user.ID,
		RecoveryCode: crypto.SecureToken(tokenLength),
		CreatedAt:    now,
	}

	return code, nil
}

// FindValidRecoveryCodes returns all valid recovery codes associated to a user
func FindValidRecoveryCodesByUser(tx *storage.Connection, user *User) ([]*RecoveryCode, error) {
	recoveryCodes := []*RecoveryCode{}
	if err := tx.Q().Where("user_id = ? AND valid = ?", user.ID, true).All(&recoveryCodes); err != nil {
		if errors.Cause(err) == sql.ErrNoRows {
			return recoveryCodes, nil
		}
		return nil, errors.Wrap(err, "Error finding recovery codes")
	}
	return recoveryCodes, nil
}
