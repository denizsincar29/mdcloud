// Package models — схема БД mdcloud.
//
// Приватность и владение живут в базе (owner_id + visibility), а не в
// структуре каталогов на диске: переезд, бэкап и раздача прав — это один
// UPDATE, а не перекладывание файлов.
package models

import (
	"time"

	"gorm.io/gorm"
)

// Видимость документа.
const (
	VisPublic  = "public"
	VisPrivate = "private"
)

// User — владелец документов и автор комментариев.
//
// Email — указатель, а не пустая строка: у уникального индекса NULL-ов
// может быть сколько угодно, а «пустая строка» — это одно конкретное
// значение, и второй пользователь без почты в него бы упёрся.
type User struct {
	ID           uint      `gorm:"primarykey" json:"id"`
	Username     string    `gorm:"uniqueIndex;size:64;not null" json:"username"`
	Email        *string   `gorm:"uniqueIndex;size:255" json:"-"`
	PasswordHash string    `gorm:"not null" json:"-"`
	DisplayName  string    `gorm:"size:120" json:"display_name"`
	IsAdmin      bool      `gorm:"default:false" json:"is_admin"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// EmailString — почта строкой (пусто, если её нет).
func (u *User) EmailString() string {
	if u == nil || u.Email == nil {
		return ""
	}
	return *u.Email
}

// Name — как подписывать этого пользователя в комментариях.
func (u *User) Name() string {
	if u == nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// Doc — один markdown-документ по адресу /<owner>/<path>.
// Path хранится нормализованным: без ведущего слэша, сегменты через "/".
type Doc struct {
	ID                  uint           `gorm:"primarykey" json:"id"`
	OwnerID             uint           `gorm:"uniqueIndex:idx_doc_owner_path;not null" json:"owner_id"`
	Owner               User           `gorm:"foreignKey:OwnerID" json:"-"`
	Path                string         `gorm:"uniqueIndex:idx_doc_owner_path;size:255;not null" json:"path"`
	Title               string         `gorm:"size:255" json:"title"`
	Content             string         `gorm:"type:text" json:"content,omitempty"`
	Visibility          string         `gorm:"size:16;not null;default:private" json:"visibility"`
	CommentsOn          bool           `gorm:"not null;default:true" json:"comments_on"`
	CommentsRequireAuth bool           `gorm:"not null;default:false" json:"comments_require_auth"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
}

// IsPublic сообщает, доступен ли документ без авторизации.
func (d *Doc) IsPublic() bool { return d.Visibility == VisPublic }

// Comment — комментарий: либо от залогиненного пользователя (AuthorID),
// либо анонимный с именем (AuthorID = nil, Name заполнен).
type Comment struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	DocID     uint      `gorm:"index;not null" json:"doc_id"`
	AuthorID  *uint     `gorm:"index" json:"author_id,omitempty"`
	Author    *User     `gorm:"foreignKey:AuthorID" json:"-"`
	Name      string    `gorm:"size:80" json:"name"` // подпись анонима либо снимок имени автора
	Body      string    `gorm:"type:text;not null" json:"body"`
	IPHash    string    `gorm:"size:64;index" json:"-"` // sha256(соль+IP), сырой IP не храним
	CreatedAt time.Time `json:"created_at"`
}

// Kind — тип записи в таблице сессий.
const (
	KindSession = "session" // обычный токен входа
	KindHandoff = "handoff" // одноразовый код перехода облако → редактор
)

// Session — и токены входа, и одноразовые коды перехода: одна таблица,
// разное время жизни и Kind. В БД лежит только хеш токена: утечка дампа
// не отдаёт готовые сессии.
type Session struct {
	TokenHash string    `gorm:"primaryKey;size:64" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	Kind      string    `gorm:"size:16;not null;default:session" json:"kind"`
	DocPath   string    `gorm:"size:255" json:"doc_path,omitempty"` // куда вести после redeem
	ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}
