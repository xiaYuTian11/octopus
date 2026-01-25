package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 3,
		Up:      backfillChannelKeyFailThreshold,
	})
}

// 003: ensure key_fail_threshold has a sane default for existing rows
func backfillChannelKeyFailThreshold(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	// Only update rows where the new column exists and is zero.
	if !db.Migrator().HasColumn("channels", "key_fail_threshold") {
		return nil
	}
	if err := db.Exec(`UPDATE channels SET key_fail_threshold = 3 WHERE key_fail_threshold = 0`).Error; err != nil {
		return fmt.Errorf("failed to backfill channels.key_fail_threshold: %w", err)
	}
	return nil
}
