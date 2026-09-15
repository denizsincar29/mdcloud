// Package store — подключение к Postgres и миграция схемы.
package store

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/denizsincar29/mdcloud/internal/models"
)

// Open подключается к базе и настраивает пул соединений.
func Open(dsn string) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("подключение к базе: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(16)
	sqlDB.SetMaxIdleConns(4)
	sqlDB.SetConnMaxLifetime(time.Hour)
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("база не отвечает: %w", err)
	}
	return db, nil
}

// Migrate создаёт/дополняет таблицы. Схема маленькая, поэтому AutoMigrate —
// осознанный выбор: деплой не требует отдельного шага с миграциями.
func Migrate(db *gorm.DB) error {
	return db.AutoMigrate(&models.User{}, &models.Doc{}, &models.Comment{}, &models.Session{})
}

// PurgeExpired удаляет протухшие сессии и коды перехода.
func PurgeExpired(db *gorm.DB) error {
	return db.Where("expires_at < ?", time.Now()).Delete(&models.Session{}).Error
}
