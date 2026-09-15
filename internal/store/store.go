// Package store — подключение к Postgres и миграция схемы.
package store

import (
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/denizsincar29/mdcloud/internal/mdpath"
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
		&models.User{}, &models.Doc{}, &models.Comment{}, &models.Session{},
		&models.Invite{}, &models.APIToken{}, &models.DocShare{})
}

// EnsureSlugs проставляет адресам латинское представление для ссылок.
//
// Колонка появилась позже самих документов, поэтому у всего, что лежало в
// облаке раньше, слаг пуст. Он выводится из пути одной функцией, так что
// это разовая уборка при старте, а не второй источник правды.
func EnsureSlugs(db *gorm.DB) error {
	var docs []models.Doc
	if err := db.Unscoped().Select("id", "path", "slug").Find(&docs).Error; err != nil {
		return err
	}
	for i := range docs {
		want := mdpath.Slug(docs[i].Path)
		if docs[i].Slug == want {
			continue
		}
		if err := db.Unscoped().Model(&models.Doc{}).Where("id = ?", docs[i].ID).
			Update("slug", want).Error; err != nil {
			return err
		}
	}
	return nil
}

// PurgeExpiredDocs стирает документы, у которых вышел срок.
//
// Документ на срок заводят, чтобы отдать его кому-то ненадолго — домашка
// учителю. Обещание «через неделю его здесь не будет» должно исполняться
// буквально, поэтому удаление жёсткое, вместе с комментариями: держать
// содержимое в таблице «на всякий случай» значило бы обещать не то, что
// делаешь.
func PurgeExpiredDocs(db *gorm.DB, now time.Time) (int64, error) {
	var docs []models.Doc
	if err := db.Unscoped().Select("id").
		Where("expires_at IS NOT NULL AND expires_at <= ?", now).
		Find(&docs).Error; err != nil {
		return 0, err
	}
	if len(docs) == 0 {
		return 0, nil
	}
	ids := make([]uint, 0, len(docs))
	for i := range docs {
		ids = append(ids, docs[i].ID)
	}
	if err := db.Where("doc_id IN ?", ids).Delete(&models.Comment{}).Error; err != nil {
		return 0, err
	}
	res := db.Unscoped().Where("id IN ?", ids).Delete(&models.Doc{})
	return res.RowsAffected, res.Error
}

// PurgeExpired удаляет протухшие сессии и просроченные API-ключи. Приглашения
// не трогаем: у них срок — это «до какого числа можно воспользоваться», а не
// «когда забыть», и список должен помнить, кому что выдали.
func PurgeExpired(db *gorm.DB) error {
	now := time.Now()
	if err := db.Where("expires_at < ?", now).Delete(&models.Session{}).Error; err != nil {
		return err
	}
	// Бессрочные ключи (expires_at IS NULL) не трогаем: их отзывают руками.
	return db.Where("expires_at IS NOT NULL AND expires_at < ?", now).
		Delete(&models.APIToken{}).Error
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
