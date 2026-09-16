// Command mdcloud — облако markdown-документов.
//
// Сервис отдаёт JSON по /api/* и (в отладочном режиме) статику из web/.
// В бою статику раздаёт Caddy, а сюда приходит только /api/*.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/denizsincar29/mdcloud/internal/api"
	"github.com/denizsincar29/mdcloud/internal/config"
	"github.com/denizsincar29/mdcloud/internal/store"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("конфигурация: %v", err)
	}

	db, err := store.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("база: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		log.Fatalf("миграция схемы: %v", err)
	}
	if err := store.EnsureSlugs(db); err != nil {
		log.Printf("латинские адреса: %v", err)
	}
	if err := store.PurgeExpired(db); err != nil {
		log.Printf("чистка сессий: %v", err)
	}
	if err := store.EnsureOwner(db, log.Printf); err != nil {
		log.Printf("хозяин облака: %v", err)
	}

	// Строка про вход: по ней в журнале видно, как настроена кука, — не
	// приходится лезть в .env, чтобы понять, почему редактор «не видит» вход.
	// Про уведомления пишем там же: «тема не задана» — частая причина того,
	// что о новом человеке никто не узнал.
	topic := cfg.NtfyTopic
	if topic == "" {
		topic = "не задана"
	}
	log.Printf("вход: кука %q домен %q secure=%v ttl=%s; регистрация открыта; ntfy: %s",
		cfg.CookieName, cfg.CookieDomain, cfg.CookieSecure, cfg.SessionTTL, topic)

	// Протухшие сессии и коды перехода подчищаем сами: таблица маленькая,
	// но копить в ней хлам незачем.
	go func() {
		for range time.Tick(6 * time.Hour) {
			if err := store.PurgeExpired(db); err != nil {
				log.Printf("чистка сессий: %v", err)
			}
		}
	}()

	// Документы на срок подметаем чаще: ссылку на такой документ уже отдали
	// человеку, и «исчезнет после четверга» должно случиться в четверг, а не
	// через полгода в ближайшую уборку. Пока подметатель не прошёл, документ
	// всё равно не открывается — срок проверяется на каждом запросе.
	go func() {
		for range time.Tick(15 * time.Minute) {
			n, err := store.PurgeExpiredDocs(db, time.Now())
			if err != nil {
				log.Printf("чистка просроченных документов: %v", err)
				continue
			}
			if n > 0 {
				log.Printf("стёрто просроченных документов: %d", n)
			}
		}
	}()

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.New(cfg, db).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("mdcloud слушает %s (облако %s, редактор %s)", cfg.Addr, cfg.BaseURL, cfg.EditorURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("сервер: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("останов: %v", err)
	}
	log.Println("mdcloud остановлен")
}
