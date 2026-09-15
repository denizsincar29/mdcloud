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
//
// Slug — тот же адрес латиницей для ссылки («ДЗ/ИИ» -> «dz/ii»). Это второе
// представление одного и того же, а не второй ключ: он выводится из Path
// одной функцией, поэтому разъехаться с ним не может. Кириллица в ссылке
// превращается в «%D0%94…», и такую ссылку нельзя ни продиктовать, ни
// прочитать с экрана.
type Doc struct {
	ID                  uint   `gorm:"primarykey" json:"id"`
	OwnerID             uint   `gorm:"uniqueIndex:idx_doc_owner_path;not null" json:"owner_id"`
	Owner               User   `gorm:"foreignKey:OwnerID" json:"-"`
	Path                string `gorm:"uniqueIndex:idx_doc_owner_path;size:255;not null" json:"path"`
	Slug                string `gorm:"index;size:255" json:"slug"`
	Title               string `gorm:"size:255" json:"title"`
	Content             string `gorm:"type:text" json:"content,omitempty"`
	Visibility          string `gorm:"size:16;not null;default:private" json:"visibility"`
	CommentsOn          bool   `gorm:"not null;default:true" json:"comments_on"`
	CommentsRequireAuth bool   `gorm:"not null;default:false" json:"comments_require_auth"`
	// ExpiresAt — документ на срок: он нужен ровно затем, чтобы отдать его
	// кому-то (домашка учителю) и не думать, что он висит в облаке вечно.
	// Пусто — документ бессрочный, как всё остальное облако.
	ExpiresAt *time.Time     `gorm:"index" json:"expires_at,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// IsPublic сообщает, доступен ли документ без авторизации.
func (d *Doc) IsPublic() bool { return d.Visibility == VisPublic }

// Expired сообщает, что срок документа вышел: он уже не открывается никому,
// а подметатель вот-вот сотрёт его совсем.
func (d *Doc) Expired(now time.Time) bool {
	return d.ExpiresAt != nil && now.After(*d.ExpiresAt)
}

// DocShare — документ, отправленный человеку по имени.
//
// Так отдают написанное адресно: учительница пишет домашку и отправляет её
// на юзернейм, не выкладывая в публичный доступ. Получателю — чтение и
// комментарии, правка остаётся за хозяином: отправленный документ не
// превращается в общий.
type DocShare struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	DocID     uint      `gorm:"uniqueIndex:idx_share_doc_user;not null" json:"doc_id"`
	UserID    uint      `gorm:"uniqueIndex:idx_share_doc_user;not null" json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

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

// Invite — одноразовое приглашение в облако.
//
// В базе лежит только хеш кода: посмотреть приглашение в дампе нельзя, а
// сам код показывается один раз, в момент создания. Отсюда же следует, что
// «напомнить ссылку» невозможно — можно только выдать новую.
type Invite struct {
	ID        uint       `gorm:"primarykey" json:"id"`
	CodeHash  string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	Note      string     `gorm:"size:120" json:"note"` // кому выдали: «Маше»
	CreatedBy uint       `gorm:"index;not null" json:"created_by"`
	UsedBy    *uint      `gorm:"index" json:"used_by,omitempty"`
	UsedAt    *time.Time `json:"used_at,omitempty"`
	ExpiresAt time.Time  `gorm:"index;not null" json:"expires_at"`
	CreatedAt time.Time  `json:"created_at"`
}

// Spent сообщает, что приглашение уже использовано.
func (i *Invite) Spent() bool { return i.UsedAt != nil }

// Expired сообщает, что срок приглашения вышел.
func (i *Invite) Expired(now time.Time) bool { return now.After(i.ExpiresAt) }

// Session — токен входа. В БД лежит только его хеш: утечка дампа не отдаёт
// готовые сессии. Браузер получает токен httpOnly-кукой, скрипты — обычным
// заголовком Authorization: Bearer.
// APIToken — ключ для скриптов и ассистентов: в отличие от сессии он не
// сгорает от выхода из браузера и живёт до срока или до отзыва. Хранится
// хешем, как сессия, поэтому показать ключ можно ровно один раз — при выдаче.
// Пустой срок — бессрочный ключ.
type APIToken struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	TokenHash  string     `gorm:"uniqueIndex;size:64;not null" json:"-"`
	UserID     uint       `gorm:"index;not null" json:"user_id"`
	Label      string     `gorm:"size:120" json:"label"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Expired сообщает, что срок ключа вышел. Бессрочный (ExpiresAt пуст) не
// протухает никогда.
func (t *APIToken) Expired(now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

type Session struct {
	TokenHash string    `gorm:"primaryKey;size:64" json:"-"`
	UserID    uint      `gorm:"index;not null" json:"user_id"`
	ExpiresAt time.Time `gorm:"index;not null" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}
