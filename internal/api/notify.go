package api

// Уведомления хозяину облака в ntfy.
//
// Регистрация в облаке открыта всем, и единственный, кому важно узнать о
// новом человеке, — хозяин: завести аккаунт может кто угодно, а вот заметить
// это должен он, а не обнаружить через месяц в списке. Уведомление едет в
// ntfy: тема — это канал, на который хозяин подписан в телефоне, и ничего
// своего (почты, SMS, мессенджеров) для этого не нужно.

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// notify шлёт короткое сообщение в тему ntfy. Тема не настроена — молчим:
// облако работает и без уведомлений, они удобство, а не условие.
//
// Отправка идёт в стороне от запроса: человек, который регистрируется, не
// должен ждать чужой сервер и уж точно не должен получать отказ из-за того,
// что ntfy недоступен. Поэтому — своя горутина, свой короткий таймаут и
// только запись в журнал при неудаче.
func (s *Server) notify(title, message string) {
	if s.cfg.NtfyTopic == "" {
		return
	}
	go func() {
		body, err := json.Marshal(map[string]any{
			"topic":   s.cfg.NtfyTopic,
			"title":   title,
			"message": message,
		})
		if err != nil {
			return
		}
		url := strings.TrimRight(s.cfg.NtfyURL, "/")
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			log.Printf("ntfy: %v", err)
			return
		}
		// Тело — JSON, а не заголовки ntfy: в заголовках не-ASCII не пролезет,
		// а в сообщении бывают и кириллица, и кавычки.
		req.Header.Set("Content-Type", "application/json")
		if s.cfg.NtfyToken != "" {
			req.Header.Set("Authorization", "Bearer "+s.cfg.NtfyToken)
		}
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("ntfy: %v", err)
			return
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			log.Printf("ntfy ответил %d", resp.StatusCode)
		}
	}()
}
