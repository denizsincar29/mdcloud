package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	"github.com/denizsincar29/mdcloud/internal/store"
)

// newTestServer поднимает сервис на sqlite в памяти: тесты не требуют
// ни Postgres, ни сети. Схема та же, что в бою (store.Migrate).
func newTestServer(t *testing.T) *httptest.Server {
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
		BaseURL:           "https://cloud.example",
		EditorURL:         "https://mathmd.example",
		AllowedOrigins:    []string{"https://mathmd.example"},
		AllowRegistration: true,
		SessionTTL:        time.Hour,
		HandoffTTL:        time.Minute,
		IPSalt:            "test-salt",
		CommentLimit:      5,
		CommentWindow:     time.Minute,
		MaxDocBytes:       1 << 20,
	}
	srv := httptest.NewServer(api.New(cfg, db).Handler())
	t.Cleanup(srv.Close)
	return srv
}

// req делает запрос и разбирает ответ как JSON-объект.
func req(t *testing.T, srv *httptest.Server, method, path, token string, body any) (int, map[string]any) {
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
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("запрос %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// register заводит пользователя и возвращает его токен.
func register(t *testing.T, srv *httptest.Server, username, password string) string {
	t.Helper()
	st, body := req(t, srv, "POST", "/api/auth/register", "", map[string]any{
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

func TestHandoffCodeIsSingleUse(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	req(t, srv, "PUT", "/api/docs/deniz/заметка", deniz, map[string]any{"content": "текст"})

	st, h := req(t, srv, "POST", "/api/handoff", deniz, map[string]any{"path": "заметка"})
	if st != http.StatusOK {
		t.Fatalf("выдача кода: %d %v", st, h)
	}
	code, _ := h["code"].(string)
	url, _ := h["url"].(string)
	if code == "" || !strings.Contains(url, "#cloud=") {
		t.Fatalf("код или ссылка пустые: %v", h)
	}
	// Код уходит во фрагменте: он не должен попадать в серверные логи.
	if strings.Contains(url, "?code=") || strings.Contains(url, "&code=") {
		t.Errorf("код обязан быть во фрагменте, а не в строке запроса: %s", url)
	}

	st, out := req(t, srv, "POST", "/api/handoff/redeem", "", map[string]any{"code": code})
	if st != http.StatusOK {
		t.Fatalf("обмен кода: %d %v", st, out)
	}
	tok, _ := out["token"].(string)
	if tok == "" || out["path"] != "заметка" {
		t.Fatalf("обмен вернул не то: %v", out)
	}
	// Токен из обмена — настоящая сессия.
	if st, me := req(t, srv, "GET", "/api/me", tok, nil); st != http.StatusOK {
		t.Errorf("токен из обмена не работает: %d %v", st, me)
	} else if user, _ := me["user"].(map[string]any); user["username"] != "deniz" {
		t.Errorf("токен выдан не тому: %v", user)
	}

	// Второй раз тот же код не срабатывает.
	if st, _ := req(t, srv, "POST", "/api/handoff/redeem", "", map[string]any{"code": code}); st != http.StatusGone {
		t.Errorf("повторный обмен кода: %d, ждали 410", st)
	}
	// И мусорный код тоже.
	if st, _ := req(t, srv, "POST", "/api/handoff/redeem", "", map[string]any{"code": "нет-такого"}); st != http.StatusGone {
		t.Errorf("обмен мусорного кода: %d, ждали 410", st)
	}
}

func TestHandoffRequiresOwnership(t *testing.T) {
	srv := newTestServer(t)
	deniz := register(t, srv, "deniz", "parol1234")
	vasya := register(t, srv, "vasilisa", "parol1234")
	req(t, srv, "PUT", "/api/docs/deniz/заметка", deniz, map[string]any{"content": "текст"})

	if st, _ := req(t, srv, "POST", "/api/handoff", vasya, map[string]any{"path": "заметка"}); st != http.StatusNotFound {
		t.Errorf("код на чужой документ: %d, ждали 404", st)
	}
	if st, _ := req(t, srv, "POST", "/api/handoff", "", map[string]any{"path": "заметка"}); st != http.StatusUnauthorized {
		t.Errorf("код без входа: %d, ждали 401", st)
	}
}

func TestRegistrationRules(t *testing.T) {
	srv := newTestServer(t)

	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"username": "Дениз", "password": "parol1234"}); st != http.StatusBadRequest {
		t.Errorf("кириллическое имя: %d, ждали 400", st)
	}
	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"username": "deniz", "password": "short"}); st != http.StatusBadRequest {
		t.Errorf("короткий пароль: %d, ждали 400", st)
	}
	register(t, srv, "deniz", "parol1234")
	if st, _ := req(t, srv, "POST", "/api/auth/register", "", map[string]any{"username": "deniz", "password": "parol1234"}); st != http.StatusConflict {
		t.Errorf("повтор имени: %d, ждали 409", st)
	}
	// Вход по имени и по почте, пароль проверяется.
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
	if resp.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Error("куки между сайтами делиться не должны — Allow-Credentials лишний")
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
