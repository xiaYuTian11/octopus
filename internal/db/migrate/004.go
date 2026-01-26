package migrate

import (
	"fmt"

	"gorm.io/gorm"
)

func init() {
	RegisterAfterAutoMigration(Migration{
		Version: 4,
		Up:      addChannelKeyRateLimitRPM,
	})
}

// 004: add rate_limit_rpm column to channel_keys table
func addChannelKeyRateLimitRPM(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("db is nil")
	}
	// Check if column already exists
	if db.Migrator().HasColumn("channel_keys", "rate_limit_rpm") {
		return nil
	}
	// Add the column with default value 0
	if err := db.Exec(`ALTER TABLE channel_keys ADD COLUMN rate_limit_rpm INTEGER DEFAULT 0`).Error; err != nil {
		return fmt.Errorf("failed to add channel_keys.rate_limit_rpm column: %w", err)
	}
	return nil
}
