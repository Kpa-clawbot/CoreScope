package mailer

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"time"
)

// SMTPConfig is the connection and sender configuration for the SMTP provider.
type SMTPConfig struct {
	Host, User, Password, FromEmail, FromName string
	Port                                      int
	TLS                                       string // "starttls" | "tls" | "none"
}

// SMTP sends mail over one SMTP connection per message. It reports no
// delivery events.
type SMTP struct {
	cfg     SMTPConfig
	rootCAs *x509.CertPool // test hook: nil means the system roots
	timeout time.Duration  // test hook: 0 means smtpTimeout
}

const smtpTimeout = 30 * time.Second

// NewSMTP returns an SMTP mailer for c.
func NewSMTP(c SMTPConfig) *SMTP {
	return &SMTP{cfg: c}
}

// Describe names host, port and TLS mode for the startup log. It never
// includes the user or password.
func (s *SMTP) Describe() string {
	return net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)) + " (" + s.cfg.TLS + ")"
}

// Send delivers m and returns its Message-ID as the provider message id.
func (s *SMTP) Send(ctx context.Context, m Message) (string, error) {
	id := newMessageID(s.cfg.FromEmail)
	msg, err := buildSMTPMessage(s.cfg.FromEmail, s.cfg.FromName, m, id, time.Now(), newBoundary())
	if err != nil {
		return "", err
	}
	c, release, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	defer release()
	if err := c.Mail(s.cfg.FromEmail); err != nil {
		return "", fmt.Errorf("smtp mail from: %w", err)
	}
	if err := c.Rcpt(m.To); err != nil {
		return "", fmt.Errorf("smtp rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(msg); err != nil {
		return "", fmt.Errorf("smtp data: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("smtp data: %w", err)
	}
	// The server accepted the message with the reply to DATA; a failed QUIT
	// does not make it undelivered, so it is not reported as a send failure.
	_ = c.Quit()
	return id, nil
}

// Events returns no events: plain SMTP has no delivery event feed.
func (s *SMTP) Events(ctx context.Context, messageID string) ([]Event, error) {
	return nil, nil
}

// Check connects, does TLS and AUTH as Send would, then QUIT. It sends no mail.
func (s *SMTP) Check(ctx context.Context) error {
	c, release, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtp quit: %w", err)
	}
	return nil
}

// open dials the server, sets up TLS for the configured mode and
// authenticates when a user is set. The whole session, until release is
// called, is bounded by the timeout and by ctx.
func (s *SMTP) open(ctx context.Context) (*smtp.Client, func(), error) {
	mode := s.cfg.TLS
	if mode != "starttls" && mode != "tls" && mode != "none" {
		return nil, nil, fmt.Errorf("smtp: unknown TLS mode %q", mode)
	}
	timeout := s.timeout
	if timeout == 0 {
		timeout = smtpTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)))
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("smtp dial: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	release := func() {
		stop()
		cancel()
		conn.Close()
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)

	c, err := s.handshake(ctx, conn, mode)
	if err != nil {
		release()
		return nil, nil, err
	}
	return c, release, nil
}

func (s *SMTP) handshake(ctx context.Context, conn net.Conn, mode string) (*smtp.Client, error) {
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, RootCAs: s.rootCAs}
	if mode == "tls" {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("smtp tls: %w", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("smtp greeting: %w", err)
	}
	if err := c.Hello(emailDomain(s.cfg.FromEmail)); err != nil {
		return nil, fmt.Errorf("smtp ehlo: %w", err)
	}
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return nil, errors.New("smtp: server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return nil, fmt.Errorf("smtp starttls: %w", err)
		}
	}
	if s.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)); err != nil {
			return nil, fmt.Errorf("smtp auth: %w", err)
		}
	}
	return c, nil
}
