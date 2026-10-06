package main

import (
	"context"
	"html"
	"log"
	"net/url"
	"strings"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

type mailContent struct {
	subject     string
	greeting    string
	paragraphs  []string
	actionLabel string
	actionURL   string
}

// link builds {publicBaseUrl}/#/account/{page}?token=… (never from the
// request Host, see the spec's Configuration section).
func (a *authService) link(page, token string) string {
	return a.set.baseURL.String() + "/#/account/" + page + "?token=" + url.QueryEscape(token)
}

func (a *authService) render(to, toName, tag string, c mailContent) mailer.Message {
	footer := "You received this because this address was used on " + a.set.baseURL.Host +
		". If that was not you, you can ignore this mail."
	var text, h strings.Builder
	text.WriteString(c.greeting + "\n\n")
	h.WriteString("<p>" + html.EscapeString(c.greeting) + "</p>")
	for _, p := range c.paragraphs {
		text.WriteString(p + "\n\n")
		h.WriteString("<p>" + html.EscapeString(p) + "</p>")
	}
	if c.actionURL != "" {
		text.WriteString(c.actionLabel + ":\n" + c.actionURL + "\n\n")
		h.WriteString(`<p><a href="` + html.EscapeString(c.actionURL) + `">` + html.EscapeString(c.actionLabel) + `</a></p>`)
	}
	text.WriteString(footer + "\n")
	h.WriteString(`<p style="color:#666;font-size:12px">` + html.EscapeString(footer) + `</p>`)
	return mailer.Message{To: to, ToName: toName, Subject: "[" + a.set.fromName + "] " + c.subject,
		HTML: h.String(), Text: text.String(), Tag: tag}
}

func (a *authService) activationMail(u *users.User, token string) mailer.Message {
	return a.render(u.Email, u.DisplayName, "activate", mailContent{
		subject: "Activate your account", greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Confirm your address to activate your account. The link works once and expires in 48 hours."},
		actionLabel: "Activate my account", actionURL: a.link("activate", token)})
}

func (a *authService) resetMail(u *users.User, token string) mailer.Message {
	return a.render(u.Email, u.DisplayName, "reset", mailContent{
		subject: "Reset your password", greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Someone asked to reset the password of your account. The link works once and expires in 1 hour. Using it logs out all your devices."},
		actionLabel: "Choose a new password", actionURL: a.link("reset", token)})
}

func (a *authService) registerNoticeMail(u *users.User) mailer.Message {
	return a.render(u.Email, u.DisplayName, "register-notice", mailContent{
		subject: "Registration attempt with your address", greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{"Someone tried to register a new account with your address, but you already have one.",
			"If it was you, log in, or use \"forgot password\" if you lost it."},
		actionLabel: "Log in", actionURL: a.set.baseURL.String() + "/#/account/login"})
}

func (a *authService) emailChangeConfirmMail(u *users.User, newEmail, token string) mailer.Message {
	return a.render(newEmail, u.DisplayName, "email-change", mailContent{
		subject: "Confirm your new address", greeting: "Hello " + u.DisplayName + ",",
		paragraphs:  []string{"Confirm that this address should replace the one on your account. The link expires in 24 hours."},
		actionLabel: "Confirm new address", actionURL: a.link("confirm-email", token)})
}

func (a *authService) emailChangeNoticeMail(u *users.User, newEmail string) mailer.Message {
	return a.render(u.Email, u.DisplayName, "email-change-notice", mailContent{
		subject: "Address change requested", greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{"A change of your account address to " + newEmail + " was requested. It takes effect only when confirmed from the new address.",
			"If this was not you, change your password now."}})
}

// sendMail sends msg and records it in the mail log. The error is logged
// (without tokens) and returned so the caller can answer 503.
func (a *authService) sendMail(ctx context.Context, u *users.User, purpose string, msg mailer.Message) error {
	id, err := a.mail.Send(ctx, msg)
	if err != nil {
		log.Printf("[users] mail %s for user #%d failed: %v", purpose, u.ID, err)
		return err
	}
	if _, err := a.st.LogMail(idPtr(u.ID), msg.To, purpose, id); err != nil {
		log.Printf("[users] mail log for user #%d: %v", u.ID, err)
	}
	return nil
}

// ingestMailEvents records provider events and flags undeliverable addresses.
// Shared by the webhook and the admin "refresh" pull.
func (a *authService) ingestMailEvents(evs []mailer.Event) {
	for _, ev := range evs {
		uid, found, err := a.st.RecordMailEvent(ev.MessageID, ev.Event, ev.At, ev.Reason)
		if err != nil {
			log.Printf("[users] record mail event: %v", err)
			continue
		}
		if found && uid != nil && mailer.IsUndeliverable(ev.Event) {
			if err := a.st.SetEmailBouncing(*uid, true); err != nil {
				log.Printf("[users] flag bouncing for user #%d: %v", *uid, err)
			}
		}
	}
}
