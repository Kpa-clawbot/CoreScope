package mailer

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSMTPOpts scripts the in-process SMTP server used by the SMTP tests.
type fakeSMTPOpts struct {
	mode          string // "plain", "starttls" or "implicit"
	offerStartTLS bool   // advertise STARTTLS in EHLO (mode "starttls" only)
	authReply     string // reply to AUTH; default "235 2.7.0 accepted"
	stallAfter    string // command verb after which the server stops answering
	certDNSName   string // when set, the certificate names only this host, not 127.0.0.1
	smtpUTF8      bool   // advertise SMTPUTF8 in EHLO
}

// fakeReceived is what the fake server saw on its connections.
type fakeReceived struct {
	commands  []string // command verbs in order, e.g. EHLO, STARTTLS, AUTH
	ehlo      string   // argument of the last EHLO
	mailFrom  string   // full MAIL line, e.g. "MAIL FROM:<x@y>"
	rcptTo    string   // full RCPT line
	authPlain string   // decoded AUTH PLAIN payload
	data      []byte   // DATA bytes, dot-unstuffed, without the final "."
}

type fakeSMTP struct {
	opts    fakeSMTPOpts
	ln      net.Listener
	tlsConf *tls.Config
	pool    *x509.CertPool

	mu    sync.Mutex
	got   fakeReceived
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
}

// newFakeSMTP starts a fake SMTP server on 127.0.0.1 with a self-signed
// certificate for that address. srv.pool trusts the certificate.
func newFakeSMTP(t *testing.T, opts fakeSMTPOpts) (string, int, *fakeSMTP) {
	t.Helper()
	if opts.authReply == "" {
		opts.authReply = "235 2.7.0 accepted"
	}
	cert, pool := selfSignedCert(t, opts.certDNSName)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &fakeSMTP{
		opts:    opts,
		ln:      ln,
		tlsConf: &tls.Config{Certificates: []tls.Certificate{cert}},
		pool:    pool,
		conns:   map[net.Conn]struct{}{},
	}
	srv.wg.Add(1)
	go srv.acceptLoop()
	t.Cleanup(func() {
		ln.Close()
		srv.mu.Lock()
		for c := range srv.conns {
			c.Close()
		}
		srv.mu.Unlock()
		srv.wg.Wait()
	})
	addr := ln.Addr().(*net.TCPAddr)
	return "127.0.0.1", addr.Port, srv
}

func (s *fakeSMTP) received() fakeReceived {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.got
	r.commands = append([]string(nil), s.got.commands...)
	r.data = append([]byte(nil), s.got.data...)
	return r
}

func (s *fakeSMTP) record(f func(r *fakeReceived)) {
	s.mu.Lock()
	f(&s.got)
	s.mu.Unlock()
}

func (s *fakeSMTP) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[c] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() {
				c.Close()
				s.mu.Lock()
				delete(s.conns, c)
				s.mu.Unlock()
			}()
			s.serve(c)
		}()
	}
}

func (s *fakeSMTP) serve(raw net.Conn) {
	var conn net.Conn = raw
	tlsActive := false
	if s.opts.mode == "implicit" {
		tc := tls.Server(raw, s.tlsConf)
		if tc.Handshake() != nil {
			return
		}
		conn, tlsActive = tc, true
	}
	r := bufio.NewReader(conn)
	reply := func(lines ...string) bool {
		_, err := io.WriteString(conn, strings.Join(lines, "\r\n")+"\r\n")
		return err == nil
	}
	if !reply("220 fake.test ESMTP") {
		return
	}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		if i := strings.IndexByte(verb, ' '); i >= 0 {
			verb = verb[:i]
		}
		if i := strings.IndexByte(verb, ':'); i >= 0 {
			verb = verb[:i]
		}
		s.record(func(g *fakeReceived) { g.commands = append(g.commands, verb) })
		if verb == s.opts.stallAfter {
			io.Copy(io.Discard, r)
			return
		}
		switch verb {
		case "EHLO":
			s.record(func(g *fakeReceived) { g.ehlo = strings.TrimSpace(line[len("EHLO"):]) })
			lines := []string{"250-fake.test"}
			if s.opts.mode == "starttls" && s.opts.offerStartTLS && !tlsActive {
				lines = append(lines, "250-STARTTLS")
			}
			if s.opts.smtpUTF8 {
				lines = append(lines, "250-SMTPUTF8")
			}
			lines = append(lines, "250 AUTH PLAIN")
			if !reply(lines...) {
				return
			}
		case "HELO", "RSET", "NOOP":
			if !reply("250 ok") {
				return
			}
		case "STARTTLS":
			if s.opts.mode != "starttls" || !s.opts.offerStartTLS || tlsActive {
				if !reply("502 5.5.1 not offered") {
					return
				}
				continue
			}
			if !reply("220 2.0.0 go ahead") {
				return
			}
			tc := tls.Server(raw, s.tlsConf)
			if tc.Handshake() != nil {
				return
			}
			conn, tlsActive = tc, true
			r = bufio.NewReader(conn)
		case "AUTH":
			fields := strings.Fields(line)
			var payload string
			if len(fields) == 3 && strings.EqualFold(fields[1], "PLAIN") {
				if b, err := base64.StdEncoding.DecodeString(fields[2]); err == nil {
					payload = string(b)
				}
			}
			s.record(func(g *fakeReceived) { g.authPlain = payload })
			if !reply(s.opts.authReply) {
				return
			}
		case "MAIL":
			s.record(func(g *fakeReceived) { g.mailFrom = line })
			if !reply("250 2.1.0 ok") {
				return
			}
		case "RCPT":
			s.record(func(g *fakeReceived) { g.rcptTo = line })
			if !reply("250 2.1.5 ok") {
				return
			}
		case "DATA":
			if !reply("354 end with .") {
				return
			}
			var data []byte
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" {
					break
				}
				data = append(data, strings.TrimPrefix(dl, ".")...)
			}
			s.record(func(g *fakeReceived) { g.data = data })
			if !reply("250 2.0.0 queued") {
				return
			}
		case "QUIT":
			reply("221 2.0.0 bye")
			return
		default:
			if !reply("502 5.5.2 unknown command") {
				return
			}
		}
	}
}

// selfSignedCert returns a CA-flagged self-signed certificate for 127.0.0.1,
// or for dnsName alone when it is set, and a pool trusting it.
func selfSignedCert(t *testing.T, dnsName string) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	if dnsName != "" {
		tmpl.Subject.CommonName = dnsName
		tmpl.DNSNames = []string{dnsName}
	} else {
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}
