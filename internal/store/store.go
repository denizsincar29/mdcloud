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
	return db.AutoMigrate(
		&models.User{}, &models.Doc{}, &models.Comment{}, &models.Session{}, &models.Invite{})
}

// PurgeExpired удаляет протухшие сессии. Приглашения не трогаем: у них
// срок — это «до какого числа можно воспользоваться», а не «когда забыть»,
// и список должен помнить, кому что выдали.
func PurgeExpired(db *gorm.DB) error {
	return db.Where("expires_at < ?", time.Now()).Delete(&models.Session{}).Error
}

// EnsureOwner назначает хозяина облака, если его нет.
//
// Право выдавать приглашения появилось позже самих аккаунтов: у облака,
// заведённого до этого, все пользователи обычные, и приглашения выписывать
// некому. Хозяин — самый первый по времени регистрации: на одно-user облаке
// это и есть владелец, а на большем выбор не хуже любого другого и виден
// в списке пользователей.
func EnsureOwner(db *gorm.DB, logf func(string, ...any)) error {
	var users, admins int64
	if err := db.Model(&models.User{}).Count(&users).Error; err != nil {
		return err
	}
	if users == 0 {
		return nil // пользователей ещё нет: хозяином станет первый зарегистрировавшийся
	}
	if err := db.Model(&models.User{}).Where("is_admin = ?", true).Count(&admins).Error; err != nil {
		return err
	}
	if admins > 0 {
		return nil
	}
	var first models.User
	if err := db.Order("id ASC").First(&first).Error; err != nil {
		return err
	}
	if err := db.Model(&models.User{}).Where("id = ?", first.ID).
		Update("is_admin", true).Error; err != nil {
		return err
	}
	logf("%s назначен хозяином облака — он выдаёт приглашения", first.Username)
	return nil
}
