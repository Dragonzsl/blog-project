package notifications

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"

	"github.com/zhushilin/blog-project/internal/operations"
	"github.com/zhushilin/blog-project/internal/platform/netguard"
)

type Message struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

type Mailer interface {
	Send(context.Context, Message) error
}

type SMTPConfig struct {
	Enabled  bool
	Host     string
	Port     int
	Username string
	Password string
	From     string
	StartTLS bool
	Timeout  time.Duration
}

type SMTPSender struct{ config SMTPConfig }

func NewSMTPSender(config SMTPConfig) (*SMTPSender, error) {
	if config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("SMTP port is invalid")
	}
	if config.Enabled && (strings.TrimSpace(config.Host) == "" || strings.TrimSpace(config.From) == "") {
		return nil, errors.New("SMTP host and from are required when mail is enabled")
	}
	if config.Timeout <= 0 {
		config.Timeout = 15 * time.Second
	}
	return &SMTPSender{config: config}, nil
}

func (s *SMTPSender) Send(ctx context.Context, message Message) error {
	if !s.config.Enabled {
		return nil
	}
	if err := validateHeader(s.config.From); err != nil {
		return err
	}
	if _, err := mail.ParseAddress(strings.TrimSpace(message.To)); err != nil {
		return fmt.Errorf("invalid recipient: %w", err)
	}
	if err := validateHeader(message.To); err != nil {
		return err
	}
	if err := validateHeader(message.Subject); err != nil {
		return err
	}
	if message.Text == "" && message.HTML == "" {
		return errors.New("mail body is required")
	}
	dial := netguard.DialContext(net.DefaultResolver, s.config.Timeout)
	connection, err := dial(ctx, "tcp", net.JoinHostPort(s.config.Host, fmt.Sprint(s.config.Port)))
	if err != nil {
		if netguard.IsPermanent(err) {
			return operations.Permanent(err)
		}
		return err
	}
	client, err := smtp.NewClient(connection, s.config.Host)
	if err != nil {
		connection.Close()
		return err
	}
	defer client.Close()
	if s.config.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: s.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return err
		}
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, s.config.Host)); err != nil {
			return err
		}
	}
	if err := client.Mail(s.config.From); err != nil {
		return err
	}
	if err := client.Rcpt(message.To); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	if _, err := io.WriteString(writer, buildMessage(s.config.From, message)); err != nil {
		writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func buildMessage(from string, message Message) string {
	if message.HTML == "" {
		return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from, message.To, message.Subject, message.Text)
	}
	text := message.Text
	if text == "" {
		text = "此邮件需要 HTML 阅读器。"
	}
	boundary := "blog-boundary"
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=%q\r\n\r\n--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n--%s--\r\n", from, message.To, message.Subject, boundary, boundary, text, boundary, message.HTML, boundary)
}

func validateHeader(value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("mail header contains a line break")
	}
	return nil
}

type NoopMailer struct{}

func (NoopMailer) Send(context.Context, Message) error { return nil }
