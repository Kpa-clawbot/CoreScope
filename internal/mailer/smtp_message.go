package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"time"
)

// buildSMTPMessage renders m as a multipart/alternative message (text part
// first, then HTML) with CRLF line ends and quoted-printable bodies. It
// rejects any CR or LF in a header name or value so a caller-supplied field
// cannot inject headers, and extra header names that are malformed or that
// the builder writes itself.
func buildSMTPMessage(fromEmail, fromName string, m Message, messageID string, date time.Time, boundary string) ([]byte, error) {
	fields := []struct{ name, value string }{
		{"from address", fromEmail}, {"from name", fromName}, {"to address", m.To},
		{"to name", m.ToName}, {"subject", m.Subject}, {"message id", messageID}, {"boundary", boundary},
	}
	for k, v := range m.Headers {
		fields = append(fields, struct{ name, value string }{"extra header name", k},
			struct{ name, value string }{"extra header value", v})
		if !headerNameRe.MatchString(k) {
			return nil, errors.New("mailer: extra header name is empty or has characters outside A-Z a-z 0-9 -")
		}
		for _, r := range reservedHeaders {
			if strings.EqualFold(k, r) {
				return nil, errors.New("mailer: extra header name collides with a header the builder writes")
			}
		}
	}
	for _, f := range fields {
		if strings.ContainsAny(f.value, "\r\n") {
			return nil, errors.New("mailer: header field " + f.name + " contains CR or LF")
		}
	}

	from := mail.Address{Name: fromName, Address: fromEmail}
	to := mail.Address{Name: m.ToName, Address: m.To}

	var buf bytes.Buffer
	line := func(s string) { buf.WriteString(s + "\r\n") }
	line("From: " + formatAddress(from))
	line("To: " + formatAddress(to))
	line("Subject: " + mime.QEncoding.Encode("UTF-8", m.Subject))
	line("Date: " + date.Format(time.RFC1123Z))
	line("Message-ID: " + messageID)
	line("MIME-Version: 1.0")
	keys := make([]string, 0, len(m.Headers))
	for k := range m.Headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		line(k + ": " + m.Headers[k])
	}
	line(`Content-Type: multipart/alternative; boundary="` + boundary + `"`)
	line("")

	for _, p := range []struct{ ctype, body string }{
		{"text/plain", m.Text},
		{"text/html", m.HTML},
	} {
		line("--" + boundary)
		line("Content-Type: " + p.ctype + "; charset=UTF-8")
		line("Content-Transfer-Encoding: quoted-printable")
		line("")
		w := quotedprintable.NewWriter(&buf)
		if _, err := w.Write([]byte(normalizeCRLF(p.body))); err != nil {
			return nil, err
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		line("")
	}
	line("--" + boundary + "--")
	return buf.Bytes(), nil
}

func normalizeCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("mailer: crypto/rand: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// newMessageID returns "<32 hex chars@domain of fromEmail>".
func newMessageID(fromEmail string) string {
	return "<" + randomHex(16) + "@" + emailDomain(fromEmail) + ">"
}

// emailDomain returns the part after the last "@", or addr when it has none.
func emailDomain(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 {
		return addr[i+1:]
	}
	return addr
}

// newBoundary returns a random multipart boundary.
func newBoundary() string {
	return "cs-" + randomHex(16)
}

// formatAddress quotes an ASCII display name through net/mail and encodes a
// non-ASCII one as a Q encoded-word, so every header uses the same charset
// spelling as the Subject.
func formatAddress(a mail.Address) string {
	for _, r := range a.Name {
		if r > 127 {
			return mime.QEncoding.Encode("UTF-8", a.Name) + " " + (&mail.Address{Address: a.Address}).String()
		}
	}
	return a.String()
}

var headerNameRe = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

var reservedHeaders = []string{"From", "To", "Subject", "Date", "Message-ID", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"}
