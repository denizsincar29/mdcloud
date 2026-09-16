package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/denizsincar29/mdcloud/internal/api"
	"github.com/denizsincar29/mdcloud/internal/config"
	"github.com/denizsincar29/mdcloud/internal/models"
	"github.com/denizsincar29/mdcloud/internal/store"
)

// newTestServer поднимает сервис на sqlite в памяти: тесты не требуют
// ни Postgres, ни сети. Схема та же, что в бою (store.Migrate).
func newTestServer(t *testing.T) *httptest.Server {
	return newTestServerWith(t, nil)
}

// newTestServerWith — тот же сервер, но с правкой настроек: тестам про
// рейтлимит нужны свои числа, тестам про куку — свой домен.
func newTestServerWith(t *testing.T, tweak func(*config.Config)) *httptest.Server {
	t.Helper()
	srv, _ := newTestServerDB(t, tweak)
	return srv
}

// newTestServerDB — сервер вместе с базой. База нужна там, где состояние
// нельзя создать через API: истёкший срок наступает сам, со временем, и
// подделать его в тесте можно только в таблице.
func newTestServerDB(t *testing.T, tweak func(*config.Config)) (*httptest.Server, *gorm.DB) {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	db, err := gorm.Open(sqlite.Open("file:"+name+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		t.Fatalf("миграция: %v", err)
	}
	cfg := &config.Config{
		BaseURL:        "https://cloud.example",
		EditorURL:      "https://mathmd.example",
		AllowedOrigins: []string{"https://mathmd.example"},
		CookieName:     "mdcloud_sid",
		SessionTTL:     time.Hour,
		IPSalt:         "test-salt",
		CommentLimit:   5,
		CommentWindow:  time.Minute,
		MaxDocBytes:    1 << 20,
		// Пределы в тестах заведомо недостижимые: проверять рейтлимит
		// должен тест про рейтлимит, а не случайный набор из сотни запросов
		// с одного адреса. Свои числа тест подставляет через tweak.
		RateLimit:   100000,
		RateWindow:  time.Minute,
		WriteLimit:  100000,
		WriteWindow: time.Minute,
	}
	if tweak != nil {
		tweak(cfg)
	}
	srv := httptest.NewServer(api.New(cfg, db).Handler())
	t.Cleanup(srv.Close)
	return srv, db
}

// opts — чем запрос отличается от обычного: токен, кука сессии, источник.
type opts struct {
	token  string // Authorization: Bearer
	cookie string // значение куки сессии
	origin string // Origin: …
}

// do делает запрос и разбирает ответ как JSON-объект.
func do(t *testing.T, srv *httptest.Server, method, path string, body any, o opts) (int, map[string]any, *http.Response) {
	t.Helper()
	var rdr *bytes.Reader
	if body == nil {
		rdr = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(raw)
	}
	r, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if o.token != "" {
		r.Header.Set("Authorization", "Bearer "+o.token)
	}
	if o.cookie != "" {
		r.AddCookie(&http.Cookie{Name: "mdcloud_sid", Value: o.cookie})
	}
	if o.origin != "" {
		r.Header.Set("Origin", o.origin)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, resp
}

// req — короткая форма do без куки и источника: так ходят скрипты.
func req(t *testing.T, srv *httptest.Server, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	st, out, _ := do(t, srv, method, path, body, opts{token: token})
	return st, out
}

// cookieOf достаёт значение куки сессии из ответа на вход или регистрацию.
func cookieOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "mdcloud_sid" && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("в ответе нет куки сессии: %v", resp.Cookies())
	return ""
}

// register заводит пользователя и возвращает его токен.
func register(t *testing.T, srv *httptest.Server, username, password string) string {
	t.Helper()
	st, body := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"consent": true,
		"username": username, "password": password, "display_name": username,
	})
	if st != http.StatusCreated {
		t.Fatalf("регистрация %s: статус %d, тело %v", username, st, body)
	}
	tok, _ := body["token"].(string)
	if tok == "" {
		t.Fatalf("регистрация %s: нет токена", username)
	}
	return tok
}

func TestDocVisibilityAndOwnership(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")

	const docPath = "/api/docs/deniz/ДЗ/ИИ/задачи"

	// Новый документ закрыт по умолчанию: содержимое ДЗ не должно утечь
	// само собой, только явной публикацией.
	st, doc := req(t, srv, "PUT", docPath, deniz, map[string]any{"content": "# Задачи"})
	if st != http.StatusOK {
		t.Fatalf("создание документа: %d %v", st, doc)
	}
	if doc["visibility"] != "private" {
		t.Errorf("новый документ должен быть private, а он %v", doc["visibility"])
	}
	if doc["can_edit"] != true {
		t.Errorf("владелец должен видеть can_edit=true: %v", doc)
	}

	// Аноним закрытый документ не видит — и не должен догадаться, что он есть.
	if st, _ := req(t, srv, "GET", docPath, "", nil); st != http.StatusNotFound {
		t.Errorf("аноним на приватный документ: %d, ждали 404", st)
	}
	// Другой залогиненный — тоже нет.
	if st, _ := req(t, srv, "GET", docPath, vasya, nil); st != http.StatusNotFound {
		t.Errorf("чужой пользователь на приватный документ: %d, ждали 404", st)
	}

	// Публикуем — теперь читают все.
	st, doc = req(t, srv, "PUT", docPath, deniz, map[string]any{"visibility": "public"})
	if st != http.StatusOK {
		t.Fatalf("публикация: %d %v", st, doc)
	}
	st, doc = req(t, srv, "GET", docPath, "", nil)
	if st != http.StatusOK {
		t.Fatalf("аноним на публичный документ: %d", st)
	}
	if doc["content"] != "# Задачи" {
		t.Errorf("содержимое не вернулось: %v", doc["content"])
	}
	if doc["can_edit"] != false {
		t.Errorf("чужому can_edit не положен: %v", doc)
	}

	// Чужой не правит и не удаляет.
	if st, _ := req(t, srv, "PUT", docPath, vasya, map[string]any{"content": "вандализм"}); st != http.StatusForbidden {
		t.Errorf("правка чужого документа: %d, ждали 403", st)
	}
	if st, _ := req(t, srv, "DELETE", docPath, vasya, nil); st != http.StatusForbidden {
		t.Errorf("удаление чужого документа: %d, ждали 403", st)
	}
	// А владелец — удаляет.
	if st, _ := req(t, srv, "DELETE", docPath, deniz, nil); st != http.StatusOK {
		t.Errorf("удаление своего документа: %d, ждали 200", st)
	}
	if st, _ := req(t, srv, "GET", docPath, deniz, nil); st != http.StatusNotFound {
		t.Errorf("удалённый документ всё ещё отдаётся: %d", st)
	}
}

// Документ «по ссылке»: открыт всякому, кто знает адрес, но в чужом списке
// документов его нет. В этом вся разница с публичным — читают одинаково,
// а на глаза попадаются по-разному.
func TestDocByLink(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")

	const docPath = "/api/docs/deniz/заметки/черновик"
	st, doc := req(t, srv, "PUT", docPath, deniz,
		map[string]any{"content": "тайное", "visibility": "link"})
	if st != http.StatusOK || doc["visibility"] != "link" {
		t.Fatalf("режим «по ссылке» не сохранился: %d %v", st, doc)
	}

	// По прямому адресу читают все: и аноним, и чужой залогиненный.
	st, got := req(t, srv, "GET", docPath, "", nil)
	if st != http.StatusOK || got["content"] != "тайное" {
		t.Errorf("аноним по ссылке: %d %v", st, got)
	}
	if st, _ := req(t, srv, "GET", docPath, vasya, nil); st != http.StatusOK {
		t.Errorf("чужой по ссылке: %d, ждали 200", st)
	}
	// Читают — но не правят: правка остаётся хозяину.
	if st, _ := req(t, srv, "PUT", docPath, vasya, map[string]any{"content": "вандализм"}); st != http.StatusForbidden {
		t.Errorf("правка чужого по ссылке: %d, ждали 403", st)
	}

	// А в чужом списке его нет: адрес и есть пропуск.
	st, body := req(t, srv, "GET", "/api/docs/deniz", vasya, nil)
	if st != http.StatusOK {
		t.Fatalf("список чужого: %d %v", st, body)
	}
	if docs, _ := body["docs"].([]any); len(docs) != 0 {
		t.Errorf("документ «по ссылке» виден в чужом списке: %v", docs)
	}
	// Хозяин видит его у себя: иначе он его и сам не найдёт.
	st, body = req(t, srv, "GET", "/api/docs", deniz, nil)
	if st != http.StatusOK {
		t.Fatalf("свой список: %d", st)
	}
	if docs, _ := body["docs"].([]any); len(docs) != 1 {
		t.Errorf("хозяин должен видеть свой документ, видит %d: %v", len(docs), docs)
	}

	// Публичный — та же открытость и вдобавок место в списке.
	if st, doc := req(t, srv, "PUT", docPath, deniz, map[string]any{"visibility": "public"}); st != http.StatusOK || doc["visibility"] != "public" {
		t.Fatalf("переход в публичный: %d %v", st, doc)
	}
	st, body = req(t, srv, "GET", "/api/docs/deniz", vasya, nil)
	if st != http.StatusOK {
		t.Fatalf("список чужого: %d", st)
	}
	if docs, _ := body["docs"].([]any); len(docs) != 1 {
		t.Errorf("публичный должен быть виден в чужом списке, видно %d", len(docs))
	}

	// Выдуманный режим не принимаем — иначе документ остался бы в состоянии,
	// которого никто не понимает.
	if st, _ := req(t, srv, "PUT", docPath, deniz, map[string]any{"visibility": "секретно"}); st != http.StatusBadRequest {
		t.Errorf("неизвестная видимость: %d, ждали 400", st)
	}
}

// Адрес документа живёт в двух видах: каким его набрал человек и каким он
// попадает в ссылку. Вести они должны к одному документу — иначе правка по
// ссылке заводила бы второй.
func TestDocOpensBySlug(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")

	st, doc := req(t, srv, "PUT", "/api/docs/deniz/ДЗ/ИИ/задачи", deniz,
		map[string]any{"content": "# Задачи", "public": true})
	if st != http.StatusOK {
		t.Fatalf("создание: %d %v", st, doc)
	}
	if doc["slug"] != "dz/ii/zadachi" {
		t.Fatalf("слаг: %v, ждали dz/ii/zadachi", doc["slug"])
	}

	st, doc = req(t, srv, "GET", "/api/docs/deniz/dz/ii/zadachi", "", nil)
	if st != http.StatusOK {
		t.Fatalf("документ по слагу: %d %v", st, doc)
	}
	if doc["path"] != "ДЗ/ИИ/задачи" {
		t.Errorf("слаг привёл не к тому документу: %v", doc["path"])
	}

	// Регистр в ссылке не важен: адрес — это адрес.
	if st, _ := req(t, srv, "GET", "/api/docs/deniz/DZ/II/ZADACHI", "", nil); st != http.StatusOK {
		t.Errorf("слаг в верхнем регистре: %d, ждали 200", st)
	}

	// Правка по слагу правит тот же документ — второй не заводится.
	if st, _ := req(t, srv, "PUT", "/api/docs/deniz/dz/ii/zadachi", deniz,
		map[string]any{"content": "новое"}); st != http.StatusOK {
		t.Fatalf("правка по слагу: %d", st)
	}
	st, list := req(t, srv, "GET", "/api/docs", deniz, nil)
	if st != http.StatusOK {
		t.Fatalf("список: %d %v", st, list)
	}
	docs, _ := list["docs"].([]any)
	if len(docs) != 1 {
		t.Errorf("правка по слагу завела второй документ: %d", len(docs))
	}

	// Удаление по слагу тоже попадает в цель.
	if st, _ := req(t, srv, "DELETE", "/api/docs/deniz/dz/ii/zadachi", deniz, nil); st != http.StatusOK {
		t.Errorf("удаление по слагу: %d, ждали 200", st)
	}
}

// Две ссылки на один документ — не ссылки. Переезд на адрес, который в
// ссылке выглядит как чужой, отбивается.
func TestSlugCollisionRefused(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")

	if st, _ := req(t, srv, "PUT", "/api/docs/deniz/ДЗ", deniz, map[string]any{"content": "домашка"}); st != http.StatusOK {
		t.Fatalf("создание ДЗ: %d", st)
	}
	if st, _ := req(t, srv, "PUT", "/api/docs/deniz/отчёт", deniz, map[string]any{"content": "отчёт"}); st != http.StatusOK {
		t.Fatalf("создание отчёта: %d", st)
	}
	st, body := req(t, srv, "PATCH", "/api/docs/deniz/отчёт", deniz, map[string]any{"path": "dz"})
	if st != http.StatusConflict {
		t.Fatalf("переезд на занятый слаг: %d %v, ждали 409", st, body)
	}
}

// Документ на срок: «через неделю его здесь не будет» — это обещание,
// которое исполняет подметатель.
func TestDocLivesUntilItsDay(t *testing.T) {
	srv, db := newTestServerDB(t, nil)
	deniz := register(t, srv, "deniz", "parol1234")

	const docPath = "/api/docs/deniz/ДЗ"
	st, doc := req(t, srv, "PUT", docPath, deniz,
		map[string]any{"content": "домашка", "public": true, "expires_in_days": 7})
	if st != http.StatusOK {
		t.Fatalf("создание на срок: %d %v", st, doc)
	}
	if doc["expires_at"] == nil {
		t.Fatal("срок не проставился")
	}
	if doc["visibility"] != "public" {
		t.Errorf("срок сломал видимость: %v", doc["visibility"])
	}

	// Срок снимается нулём — документ остаётся насовсем.
	st, doc = req(t, srv, "PUT", docPath, deniz, map[string]any{"expires_in_days": 0})
	if st != http.StatusOK {
		t.Fatalf("снятие срока: %d %v", st, doc)
	}
	if doc["expires_at"] != nil {
		t.Errorf("срок не снялся: %v", doc["expires_at"])
	}
	// Опечатка в сроке — отказ, а не «вечность» незаметно.
	if st, _ := req(t, srv, "PUT", docPath, deniz, map[string]any{"expires_in_days": 100000}); st != http.StatusBadRequest {
		t.Errorf("срок в 274 года: %d, ждали 400", st)
	}

	// Истёкшее состояние приходит со временем, поэтому ставим его в таблице.
	past := time.Now().Add(-time.Hour)
	if err := db.Model(&models.Doc{}).Where("path = ?", "ДЗ").
		Update("expires_at", past).Error; err != nil {
		t.Fatalf("подделка срока: %v", err)
	}
	if st, _ := req(t, srv, "GET", docPath, "", nil); st != http.StatusNotFound {
		t.Errorf("истёкший документ отдаётся анониму: %d, ждали 404", st)
	}
	if st, _ := req(t, srv, "GET", docPath, deniz, nil); st != http.StatusNotFound {
		t.Errorf("истёкший документ отдаётся хозяину: %d, ждали 404", st)
	}
	st, list := req(t, srv, "GET", "/api/docs", deniz, nil)
	if st != http.StatusOK {
		t.Fatalf("список: %d", st)
	}
	if docs, _ := list["docs"].([]any); len(docs) != 0 {
		t.Errorf("истёкший документ висит в списке: %d", len(docs))
	}

	// Подметатель стирает его совсем.
	n, err := store.PurgeExpiredDocs(db, time.Now())
	if err != nil {
		t.Fatalf("подметатель: %v", err)
	}
	if n != 1 {
		t.Errorf("подметатель стёр %d документов, ждали 1", n)
	}
	var left int64
	if err := db.Unscoped().Model(&models.Doc{}).Count(&left).Error; err != nil {
		t.Fatalf("подсчёт: %v", err)
	}
	if left != 0 {
		t.Errorf("после подметателя осталось документов: %d", left)
	}
}

func TestListByOwnerShowsOnlyPublic(t *testing.T) {
	srv := newTestServer(t)
	tok := register(t, srv, "deniz", "parol1234")

	req(t, srv, "PUT", "/api/docs/deniz/публичное", tok, map[string]any{"content": "раз", "visibility": "public"})
	req(t, srv, "PUT", "/api/docs/deniz/секретное", tok, map[string]any{"content": "два"})

	st, body := req(t, srv, "GET", "/api/docs/deniz", "", nil)
	if st != http.StatusOK {
		t.Fatalf("список: %d %v", st, body)
	}
	docs, _ := body["docs"].([]any)
	if len(docs) != 1 {
		t.Fatalf("аноним должен видеть один документ, видит %d: %v", len(docs), docs)
	}

	// Хозяин в своём списке видит оба.
	st, body = req(t, srv, "GET", "/api/docs", tok, nil)
	if st != http.StatusOK {
		t.Fatalf("свой список: %d", st)
	}
	if docs, _ := body["docs"].([]any); len(docs) != 2 {
		t.Errorf("хозяин должен видеть два документа, видит %d", len(docs))
	}

	if st, _ := req(t, srv, "GET", "/api/docs/незнакомец", "", nil); st != http.StatusNotFound {
		t.Errorf("список несуществующего пользователя: %d, ждали 404", st)
	}
}

func TestComments(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")

	const docPath = "/api/docs/deniz/заметка"
	req(t, srv, "PUT", docPath, deniz, map[string]any{"content": "текст", "visibility": "public"})
	const comments = "/api/comments/deniz/заметка"

	// Аноним обязан представиться.
	if st, _ := req(t, srv, "POST", comments, "", map[string]any{"body": "привет"}); st != http.StatusBadRequest {
		t.Errorf("аноним без имени: %d, ждали 400", st)
	}
	st, c := req(t, srv, "POST", comments, "", map[string]any{"body": "привет", "name": "Гость"})
	if st != http.StatusCreated {
		t.Fatalf("анонимный комментарий: %d %v", st, c)
	}
	if c["anonymous"] != true || c["author_name"] != "Гость" {
		t.Errorf("анонимный комментарий выглядит не так: %v", c)
	}

	// Залогиненный подписывается сам.
	vasya := register(t, srv, "vasilisa", "parol1234")
	if st, _ := req(t, srv, "POST", comments, vasya, map[string]any{"body": "а я вошла"}); st != http.StatusCreated {
		t.Errorf("комментарий от пользователя: %d", st)
	}

	st, list := req(t, srv, "GET", comments, "", nil)
	if st != http.StatusOK {
		t.Fatalf("список комментариев: %d", st)
	}
	if cs, _ := list["comments"].([]any); len(cs) != 2 {
		t.Errorf("ждали два комментария, получили %d", len(cs))
	}

	// Автор удаляет свой, чужой — нет.
	var vasyaComment float64
	for _, raw := range list["comments"].([]any) {
		m := raw.(map[string]any)
		if m["author_name"] == "vasilisa" {
			vasyaComment = m["id"].(float64)
		}
	}
	anon := fmt.Sprintf("/api/comments/%d", int(vasyaComment))
	if st, _ := req(t, srv, "DELETE", anon, deniz, nil); st != http.StatusOK {
		t.Errorf("хозяин документа не смог убрать чужой комментарий: %d", st)
	}

	// Требование входа работает.
	req(t, srv, "PUT", docPath, deniz, map[string]any{"comments_require_auth": true})
	if st, _ := req(t, srv, "POST", comments, "", map[string]any{"body": "пустите", "name": "Гость"}); st != http.StatusUnauthorized {
		t.Errorf("комментарий без входа там, где нужен вход: %d, ждали 401", st)
	}

	// Выключенные комментарии закрывают и чтение формы, и запись.
	req(t, srv, "PUT", docPath, deniz, map[string]any{"comments_on": false})
	if st, _ := req(t, srv, "POST", comments, deniz, map[string]any{"body": "поздно"}); st != http.StatusForbidden {
		t.Errorf("комментарий при выключенных комментариях: %d, ждали 403", st)
	}
}

func TestCommentsOnPrivateDocAreHidden(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	req(t, srv, "PUT", "/api/docs/deniz/тайна", deniz, map[string]any{"content": "тсс"})

	if st, _ := req(t, srv, "GET", "/api/comments/deniz/тайна", "", nil); st != http.StatusNotFound {
		t.Errorf("комментарии закрытого документа: %d, ждали 404", st)
	}
	if st, _ := req(t, srv, "POST", "/api/comments/deniz/тайна", "", map[string]any{"body": "ау", "name": "Гость"}); st != http.StatusNotFound {
		t.Errorf("комментарий в закрытый документ: %d, ждали 404", st)
	}
}

// Первый зарегистрировавшийся — хозяин облака: без него в облаке некому
// вести учётные записи. Второй приходит на общих правах — регистрация открыта.
func TestFirstUserIsAdmin(t *testing.T) {
	srv := newTestServer(t)

	st, body := req(t, srv, "POST", "/api/auth/register", "",
		map[string]any{"consent": true, "username": "deniz", "password": "parol1234"})
	if st != http.StatusCreated {
		t.Fatalf("регистрация хозяина: %d %v", st, body)
	}
	user, _ := body["user"].(map[string]any)
	if user["is_admin"] != true {
		t.Errorf("первый пользователь должен быть админом: %v", user)
	}
	// Почты в ответе нет: её у аккаунта больше нет.
	if _, ok := user["email"]; ok {
		t.Errorf("в ответе про аккаунт не должно быть почты: %v", user)
	}

	st, body = req(t, srv, "POST", "/api/auth/register", "",
		map[string]any{"consent": true, "username": "vasilisa", "password": "parol1234"})
	if st != http.StatusCreated {
		t.Fatalf("регистрация второго: %d %v", st, body)
	}
	if user, _ := body["user"].(map[string]any); user["is_admin"] != false {
		t.Errorf("второй не должен быть админом: %v", user)
	}
}

// Приглашений в облаке больше нет — ни адресов, ни таблицы, ни колонки почты.
func TestInvitesAndEmailAreGone(t *testing.T) {
	srv, db := newTestServerDB(t, nil)
	deniz := register(t, srv, "deniz", "parol1234")

	for _, path := range []string{"/api/invites", "/api/invites/1"} {
		if st, _ := req(t, srv, "GET", path, deniz, nil); st != http.StatusNotFound {
			t.Errorf("GET %s: %d, ждали 404", path, st)
		}
	}
	if db.Migrator().HasTable("invites") {
		t.Error("таблица приглашений должна была исчезнуть")
	}
	if db.Migrator().HasColumn(&models.User{}, "email") {
		t.Error("колонка почты должна была исчезнуть")
	}
}

// accountID — номер аккаунта по его ключу: списку хозяина и действиям над
// учёткой нужен номер, а наружу его отдаёт только /api/me.
func accountID(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	st, body := req(t, srv, "GET", "/api/me", token, nil)
	if st != http.StatusOK {
		t.Fatalf("кто я: %d %v", st, body)
	}
	user, _ := body["user"].(map[string]any)
	return fmt.Sprintf("%v", user["id"])
}

// Хозяин видит все учётные записи и может убрать лишнюю — вместе со всем,
// что человек написал.
func TestAdminDeletesAccount(t *testing.T) {
	srv, db := newTestServerDB(t, nil)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")
	req(t, srv, "PUT", "/api/docs/vasilisa/дневник", vasya, map[string]any{"content": "привет"})

	st, list := req(t, srv, "GET", "/api/admin/users", deniz, nil)
	if st != http.StatusOK {
		t.Fatalf("список аккаунтов: %d %v", st, list)
	}
	users, _ := list["users"].([]any)
	if len(users) != 2 {
		t.Fatalf("в списке %d аккаунтов, ждали два: %v", len(users), list)
	}
	first, _ := users[0].(map[string]any)
	if first["username"] != "deniz" || first["me"] != true || first["is_admin"] != true {
		t.Errorf("хозяин в списке выглядит не так: %v", first)
	}
	second, _ := users[1].(map[string]any)
	if second["username"] != "vasilisa" || second["docs"] != float64(1) {
		t.Errorf("Василиса в списке выглядит не так: %v", second)
	}

	// Себя удалять нечем: хозяин — тот, кто ведёт учётные записи.
	if st, _ := req(t, srv, "DELETE", "/api/admin/users/"+accountID(t, srv, deniz), deniz, nil); st != http.StatusBadRequest {
		t.Errorf("удаление себя: %d, ждали 400", st)
	}

	if st, _ := req(t, srv, "DELETE", "/api/admin/users/"+accountID(t, srv, vasya), deniz, nil); st != http.StatusOK {
		t.Fatalf("удаление учётки: %d", st)
	}
	// Учётка ушла целиком: и вход, и написанное.
	if st, _ := req(t, srv, "GET", "/api/me", vasya, nil); st != http.StatusUnauthorized {
		t.Errorf("ключ удалённого: %d, ждали 401", st)
	}
	if st, _ := req(t, srv, "GET", "/api/docs/vasilisa/дневник", deniz, nil); st != http.StatusNotFound {
		t.Errorf("документ удалённого: %d, ждали 404", st)
	}
	var docs int64
	db.Unscoped().Model(&models.Doc{}).Where("path = ?", "дневник").Count(&docs)
	if docs != 0 {
		t.Errorf("документы удалённого остались: %d", docs)
	}
}

// Учётные записи — дело хозяина: остальным туда нельзя.
func TestAdminRoutesAreAdminOnly(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")

	if st, _ := req(t, srv, "GET", "/api/admin/users", vasya, nil); st != http.StatusForbidden {
		t.Errorf("чужой смотрит список аккаунтов: %d, ждали 403", st)
	}
	if st, _ := req(t, srv, "DELETE", "/api/admin/users/1", vasya, nil); st != http.StatusForbidden {
		t.Errorf("чужой удаляет учётку: %d, ждали 403", st)
	}
	if st, _ := req(t, srv, "GET", "/api/admin/users", "", nil); st != http.StatusUnauthorized {
		t.Errorf("список аккаунтов без входа: %d, ждали 401", st)
	}
	if st, _ := req(t, srv, "GET", "/api/admin/users", deniz, nil); st != http.StatusOK {
		t.Errorf("хозяин смотрит список аккаунтов: %d", st)
	}
}

// Выгнать — значит выгнать: и из браузеров, и из скриптов. Оставить ключи
// живыми значило бы выставить человека за дверь, оставив ему ключ.
func TestAdminKicksAccount(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")

	st, key, _ := do(t, srv, "POST", "/api/tokens", map[string]any{"label": "скрипт"}, opts{token: vasya})
	if st != http.StatusCreated {
		t.Fatalf("выдача ключа: %d %v", st, key)
	}
	apiKey, _ := key["token"].(string)

	id := accountID(t, srv, vasya)
	st, body, _ := do(t, srv, "POST", "/api/admin/users/"+id+"/logout", nil, opts{token: deniz})
	if st != http.StatusOK {
		t.Fatalf("выгнать: %d %v", st, body)
	}
	if st, _ := req(t, srv, "GET", "/api/me", vasya, nil); st != http.StatusUnauthorized {
		t.Errorf("сессия выгнанного жива: %d, ждали 401", st)
	}
	if st, _ := req(t, srv, "GET", "/api/me", apiKey, nil); st != http.StatusUnauthorized {
		t.Errorf("ключ выгнанного жив: %d, ждали 401", st)
	}
}

// Права хозяина раздаются и отзываются — но хозяин в облаке остаётся
// всегда. Держится это на одном отказе: себя разжаловать нельзя, а кто может
// разжаловать другого, тот сам хозяин, то есть в облаке нас двое.
func TestAdminGrantsAndRevokesRights(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")
	denizID := accountID(t, srv, deniz)
	vasyaID := accountID(t, srv, vasya)

	if st, _, _ := do(t, srv, "POST", "/api/admin/users/"+vasyaID+"/admin",
		map[string]any{"admin": true}, opts{token: deniz}); st != http.StatusOK {
		t.Fatalf("назначение хозяином: %d", st)
	}
	if st, _ := req(t, srv, "GET", "/api/admin/users", vasya, nil); st != http.StatusOK {
		t.Errorf("назначенный не видит список: %d", st)
	}

	// Себя разжаловать нельзя: тот, кто ведёт учётные записи, не должен
	// потерять права одним нажатием — иначе в облаке не останется хозяина.
	if st, _, _ := do(t, srv, "POST", "/api/admin/users/"+denizID+"/admin",
		map[string]any{"admin": false}, opts{token: deniz}); st != http.StatusBadRequest {
		t.Errorf("разжалование себя: %d, ждали 400", st)
	}
	// А другого — можно, и он сразу теряет доступ к учётным записям.
	if st, _, _ := do(t, srv, "POST", "/api/admin/users/"+vasyaID+"/admin",
		map[string]any{"admin": false}, opts{token: deniz}); st != http.StatusOK {
		t.Errorf("разжалование назначенного: %d", st)
	}
	if st, _ := req(t, srv, "GET", "/api/admin/users", vasya, nil); st != http.StatusForbidden {
		t.Errorf("разжалованный видит список: %d, ждали 403", st)
	}
}

// Общий предел на /api: один адрес не заваливает облако запросами.
func TestRateLimitOnAPI(t *testing.T) {
	srv := newTestServerWith(t, func(c *config.Config) {
		c.RateLimit = 5
		c.RateWindow = time.Minute
	})
	for i := 0; i < 5; i++ {
		if st, _ := req(t, srv, "GET", "/api/health", "", nil); st != http.StatusOK {
			t.Fatalf("запрос %d: %d, ждали 200", i+1, st)
		}
	}
	st, body, resp := do(t, srv, "GET", "/api/health", nil, opts{})
	if st != http.StatusTooManyRequests {
		t.Fatalf("шестой запрос: %d %v, ждали 429", st, body)
	}
	// Отказ должен говорить, когда возвращаться: иначе скрипт будет долбить
	// наугад.
	if resp.Header.Get("Retry-After") == "" {
		t.Error("в отказе нет Retry-After")
	}
	if _, ok := body["error"]; !ok {
		t.Errorf("отказ без объяснения: %v", body)
	}
}

// Отдельный предел на то, что меняет данные: читать можно подряд, писать
// пачками — нет.
func TestWriteLimit(t *testing.T) {
	srv := newTestServerWith(t, func(c *config.Config) {
		c.RateLimit = 1000
		c.WriteLimit = 3
		c.WriteWindow = time.Minute
	})
	// Регистрация — тоже запись, и она тратит первое место в окне.
	deniz := register(t, srv, "deniz", "parol1234")
	if st, _ := req(t, srv, "PUT", "/api/docs/deniz/раз", deniz, map[string]any{"content": "а"}); st != http.StatusOK {
		t.Fatalf("первый документ: %d", st)
	}
	if st, _ := req(t, srv, "PUT", "/api/docs/deniz/два", deniz, map[string]any{"content": "б"}); st != http.StatusOK {
		t.Fatalf("второй документ: %d", st)
	}
	if st, _ := req(t, srv, "PUT", "/api/docs/deniz/три", deniz, map[string]any{"content": "в"}); st != http.StatusTooManyRequests {
		t.Errorf("третья запись сверх предела: %d, ждали 429", st)
	}
	// Чтение при этом работает: предел на запись не должен мешать читать.
	if st, _ := req(t, srv, "GET", "/api/docs/deniz/раз", deniz, nil); st != http.StatusOK {
		t.Errorf("чтение под пределом на запись: %d", st)
	}
}

// О новой регистрации хозяин узнаёт из ntfy: регистрация открыта, и это
// единственная новость, которую стоит рассказывать сразу.
func TestRegistrationNotifiesOwner(t *testing.T) {
	got := make(chan []byte, 1)
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case got <- body:
		default:
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ntfy.Close()

	srv := newTestServerWith(t, func(c *config.Config) {
		c.NtfyURL = ntfy.URL
		c.NtfyTopic = "облако"
	})
	register(t, srv, "deniz", "parol1234")

	select {
	case body := <-got:
		var msg map[string]any
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("уведомление не разобралось: %v (%s)", err, body)
		}
		if msg["topic"] != "облако" {
			t.Errorf("тема в уведомлении: %v", msg["topic"])
		}
		if text, _ := msg["message"].(string); !strings.Contains(text, "deniz") {
			t.Errorf("в уведомлении нет имени: %v", msg["message"])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("уведомление в ntfy не пришло")
	}
}

// Вход кладёт сессию в httpOnly-куку, и дальше браузер ходит ею одной.
func TestCookieSessionAndCSRF(t *testing.T) {
	srv := newTestServer(t)
	register(t, srv, "deniz", "parol1234")

	st, _, resp := do(t, srv, "POST", "/api/auth/login",
		map[string]any{"login": "deniz", "password": "parol1234"},
		opts{origin: "https://cloud.example"})
	if st != http.StatusOK {
		t.Fatalf("вход: %d", st)
	}
	var sc *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "mdcloud_sid" {
			sc = c
		}
	}
	if sc == nil || sc.Value == "" {
		t.Fatalf("вход не выдал куку сессии: %v", resp.Cookies())
	}
	// Флаги куки — не украшение: без них её достанет любой скрипт на странице.
	if !sc.HttpOnly {
		t.Error("кука сессии обязана быть HttpOnly")
	}
	if sc.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite=Lax, а не %v: иначе кука уедет на чужой сайт", sc.SameSite)
	}
	if sc.Path != "/" {
		t.Errorf("путь куки %q, ждали /", sc.Path)
	}
	cookie := sc.Value

	if st, _, _ := do(t, srv, "GET", "/api/me", nil, opts{cookie: cookie}); st != http.StatusOK {
		t.Errorf("запрос с кукой: %d, ждали 200", st)
	}

	// Чужой сайт с нашей кукой — отказ: браузер приложил бы её сам.
	st, _, _ = do(t, srv, "PUT", "/api/docs/deniz/заметка", map[string]any{"content": "чужое"},
		opts{cookie: cookie, origin: "https://злой.example"})
	if st != http.StatusForbidden {
		t.Errorf("запись с чужого сайта: %d, ждали 403", st)
	}
	// Запрос с кукой вообще без Origin — тоже отказ: браузер Origin ставит
	// всегда, а его отсутствие означает самодельный клиент с чужой кукой.
	st, _, _ = do(t, srv, "PUT", "/api/docs/deniz/заметка", map[string]any{"content": "чужое"},
		opts{cookie: cookie})
	if st != http.StatusForbidden {
		t.Errorf("запись без Origin: %d, ждали 403", st)
	}
	// Свой редактор — свой: запись проходит.
	st, _, _ = do(t, srv, "PUT", "/api/docs/deniz/заметка", map[string]any{"content": "своё"},
		opts{cookie: cookie, origin: "https://mathmd.example"})
	if st != http.StatusOK {
		t.Errorf("запись со своего сайта: %d, ждали 200", st)
	}
	// А чтения под CSRF не попадают: их чужой сайт и так не увидит.
	if st, _, _ := do(t, srv, "GET", "/api/me", nil, opts{cookie: cookie, origin: "https://злой.example"}); st != http.StatusOK {
		t.Errorf("чтение с чужого Origin: %d, ждали 200", st)
	}

	// Выход гасит и куку, и сессию.
	st, _, resp = do(t, srv, "POST", "/api/auth/logout", nil,
		opts{cookie: cookie, origin: "https://cloud.example"})
	if st != http.StatusOK {
		t.Fatalf("выход: %d", st)
	}
	cleared := false
	for _, c := range resp.Cookies() {
		if c.Name == "mdcloud_sid" && c.Value == "" {
			cleared = true
		}
	}
	if !cleared {
		t.Errorf("выход не снял куку: %v", resp.Cookies())
	}
	if st, _, _ := do(t, srv, "GET", "/api/me", nil, opts{cookie: cookie}); st != http.StatusUnauthorized {
		t.Errorf("сессия после выхода: %d, ждали 401", st)
	}
}

// Форма с чужого сайта до API не доходит: JSON ей не отправить без
// разрешения, которого чужому сайту не выдают.
func TestFormPostRejected(t *testing.T) {
	srv := newTestServer(t)
	register(t, srv, "deniz", "parol1234")

	r, err := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/login",
		strings.NewReader("login=deniz&password=parol1234"))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", "https://злой.example")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("форма на вход: %d, ждали 415", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Errorf("форма получила куку сессии: %v", resp.Cookies())
	}
}

// Bearer-токен остаётся рабочим: им ходят скрипты и сам редактор, и CSRF
// к ним не относится — куки у них нет.
func TestBearerStillWorks(t *testing.T) {
	srv := newTestServer(t)
	tok := register(t, srv, "deniz", "parol1234")

	st, _, _ := do(t, srv, "PUT", "/api/docs/deniz/заметка", map[string]any{"content": "текст"},
		opts{token: tok})
	if st != http.StatusOK {
		t.Errorf("запись с Bearer без Origin: %d, ждали 200", st)
	}
}

func TestRegistrationRules(t *testing.T) {
	srv := newTestServer(t)

	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"consent": true, "username": "Дениз", "password": "parol1234"}); st != http.StatusBadRequest {
		t.Errorf("кириллическое имя: %d, ждали 400", st)
	}
	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"consent": true, "username": "deniz", "password": "short"}); st != http.StatusBadRequest {
		t.Errorf("короткий пароль: %d, ждали 400", st)
	}
	// Без согласия с политикой аккаунт не заводится: согласие — правовое
	// основание обработки, и подтвердить его будет нечем.
	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"username": "bezgalki", "password": "parol1234"}); st != http.StatusBadRequest {
		t.Errorf("регистрация без согласия: %d, ждали 400", st)
	}
	register(t, srv, "deniz", "parol1234")
	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"consent": true, "username": "deniz", "password": "parol1234"}); st != http.StatusConflict {
		t.Errorf("повтор имени: %d, ждали 409", st)
	}
	// Вход по имени: почты у аккаунта нет, вход один.
	if st, _ := req(t, srv, "POST", "/api/auth/login", "", map[string]any{"login": "deniz", "password": "неверный"}); st != http.StatusUnauthorized {
		t.Errorf("неверный пароль: %d, ждали 401", st)
	}
	if st, body := req(t, srv, "POST", "/api/auth/login", "", map[string]any{"login": "deniz", "password": "parol1234"}); st != http.StatusOK {
		t.Errorf("вход: %d %v", st, body)
	}
}

func TestTokenLifecycle(t *testing.T) {
	srv := newTestServer(t)
	tok := register(t, srv, "deniz", "parol1234")

	if st, _ := req(t, srv, "GET", "/api/me", "", nil); st != http.StatusUnauthorized {
		t.Errorf("без токена: %d, ждали 401", st)
	}
	if st, _ := req(t, srv, "GET", "/api/me", "мусорный-токен", nil); st != http.StatusUnauthorized {
		t.Errorf("мусорный токен: %d, ждали 401", st)
	}
	if st, _ := req(t, srv, "GET", "/api/me", tok, nil); st != http.StatusOK {
		t.Errorf("свой токен: %d", st)
	}
	if st, _ := req(t, srv, "POST", "/api/auth/logout", tok, nil); st != http.StatusOK {
		t.Errorf("выход: %d", st)
	}
	if st, _ := req(t, srv, "GET", "/api/me", tok, nil); st != http.StatusUnauthorized {
		t.Errorf("токен после выхода: %d, ждали 401", st)
	}
}

func TestCORSPreflight(t *testing.T) {
	srv := newTestServer(t)

	r, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/docs/deniz", nil)
	r.Header.Set("Origin", "https://mathmd.example")
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://mathmd.example" {
		t.Errorf("свой источник не пропущен: %q", got)
	}
	// Редактор ходит кукой сессии, поэтому источник отражается точным
	// значением, а кредам нужно разрешение — со звёздочкой браузер откажет.
	if resp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("редактору нужен Allow-Credentials: он ходит кукой сессии")
	}

	r2, _ := http.NewRequest(http.MethodOptions, srv.URL+"/api/docs/deniz", nil)
	r2.Header.Set("Origin", "https://злой.example")
	resp2, err := http.DefaultClient.Do(r2)
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("чужой источник получил разрешение: %q", got)
	}
}
