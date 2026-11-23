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
	ss, err := s.open(ctx)
	if err != nil {
		return "", err
	}
	defer ss.release()
	// net/smtp adds the SMTPUTF8 parameter to MAIL FROM when the server
	// offers it; without it a non-ASCII address gets an opaque server error.
	if ok, _ := ss.c.Extension("SMTPUTF8"); !ok {
		if !isASCII(s.cfg.FromEmail) {
			return "", errors.New("smtp: the sender address is not ASCII and the server does not support SMTPUTF8")
		}
		if !isASCII(m.To) {
			return "", errors.New("smtp: the recipient address is not ASCII and the server does not support SMTPUTF8")
		}
	}
	if err := ss.c.Mail(s.cfg.FromEmail); err != nil {
		return "", ss.fail("mail from", err)
	}
	if err := ss.c.Rcpt(m.To); err != nil {
		return "", ss.fail("rcpt to", err)
	}
	w, err := ss.c.Data()
	if err != nil {
		return "", ss.fail("data", err)
	}
	if _, err := w.Write(msg); err != nil {
		return "", ss.fail("data", err)
	}
	if err := w.Close(); err != nil {
		return "", ss.fail("data", err)
	}
	// The server accepted the message with the reply to DATA; a failed QUIT
	// does not make it undelivered, so it is not reported as a send failure.
	_ = ss.c.Quit()
	return id, nil
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// Events returns no events: plain SMTP has no delivery event feed.
func (s *SMTP) Events(ctx context.Context, messageID string) ([]Event, error) {
	return nil, nil
}

// Check connects, does TLS and AUTH as Send would, then QUIT. It sends no mail.
func (s *SMTP) Check(ctx context.Context) error {
	ss, err := s.open(ctx)
	if err != nil {
		return err
	}
	defer ss.release()
	// Unlike Send, a failed QUIT is returned: a check should see a clean session end.
	if err := ss.c.Quit(); err != nil {
		return ss.fail("quit", err)
	}
	return nil
}

// smtpSession is one open connection, bounded by ctx (the timeout and the
// caller's cancellation).
type smtpSession struct {
	c        *smtp.Client
	ctx      context.Context
	deadline time.Time
	release  func()
}

// fail wraps err for step. When the session ran out of time or was
// cancelled, the I/O error it caused (an i/o timeout or a closed
// connection) is replaced by context.DeadlineExceeded or context.Canceled.
func (ss *smtpSession) fail(step string, err error) error {
	if cerr := ss.ctx.Err(); cerr != nil {
		err = cerr
	} else if !time.Now().Before(ss.deadline) {
		// The connection deadline can fire just before the context's timer.
		err = context.DeadlineExceeded
	}
	return fmt.Errorf("smtp %s: %w", step, err)
}

// open dials the server, sets up TLS for the configured mode and
// authenticates when a user is set. The whole session, until release is
// called, is bounded by the timeout and by ctx.
func (s *SMTP) open(ctx context.Context) (*smtpSession, error) {
	mode := s.cfg.TLS
	if mode != "starttls" && mode != "tls" && mode != "none" {
		return nil, fmt.Errorf("smtp: unknown TLS mode %q", mode)
	}
	timeout := s.timeout
	if timeout == 0 {
		timeout = smtpTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	deadline, _ := ctx.Deadline()
	ss := &smtpSession{ctx: ctx, deadline: deadline, release: cancel}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port)))
	if err != nil {
		err = ss.fail("dial", err) // before cancel, which would make every dial error read as canceled
		cancel()
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	ss.release = func() {
		stop()
		cancel()
		conn.Close()
	}
	conn.SetDeadline(deadline)

	if err := s.handshake(ss, conn, mode); err != nil {
		ss.release()
		return nil, err
	}
	return ss, nil
}

func (s *SMTP) handshake(ss *smtpSession, conn net.Conn, mode string) error {
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, RootCAs: s.rootCAs}
	if mode == "tls" {
		tc := tls.Client(conn, tlsConfig)
		if err := tc.HandshakeContext(ss.ctx); err != nil {
			return ss.fail("tls", err)
		}
		conn = tc
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return ss.fail("greeting", err)
	}
	ss.c = c
	if err := c.Hello(emailDomain(s.cfg.FromEmail)); err != nil {
		return ss.fail("ehlo", err)
	}
	if mode == "starttls" {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("smtp: server does not offer STARTTLS")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return ss.fail("starttls", err)
		}
	}
	if s.cfg.User != "" {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.User, s.cfg.Password, s.cfg.Host)); err != nil {
			return ss.fail("auth", err)
		}
	}
	return nil
}
