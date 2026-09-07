// Package payment turns a membership fee into the two ways the association
// takes money: a bankgiro transfer, and Swish.
//
// Neither is a payment integration. Nothing here talks to a bank, learns
// whether money arrived, or could. The association's cashier reads the bank
// statement and ticks people off in the register, exactly as before — all
// this does is spare somebody typing an account number, an amount and a
// reference into their banking app by hand, and getting one of the three
// wrong.
package payment

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"rsc.io/qr"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// Ways is everything somebody needs in order to pay, in whichever way suits.
type Ways struct {
	Year      int
	AmountKr  int
	Reference string
	// Bankgiro is empty when the association has not configured one.
	Bankgiro string
	// Swish is empty when there is no Swish number.
	Swish Swish
}

// Swish is the association's Swish number and the QR code that fills the app
// in for you.
type Swish struct {
	// Number as a person writes it, for reading aloud and typing.
	Number string
	// QR is a data: URI holding the PNG, so the page needs no second request
	// and no external host. The Content-Security-Policy allows data: images
	// for exactly this.
	QR string
	// Link is what the QR encodes: an app.swish.nu address. Worth having on
	// its own, because a QR code is useless to somebody already reading the
	// page on the phone they would pay with — they cannot scan their own
	// screen. Following the link opens the same prefilled payment.
	Link string
}

// Offered reports whether Swish is on offer at all.
func (s Swish) Offered() bool { return s.Number != "" }

// For builds the ways to pay a year's fee for one person.
func For(m config.Membership, kind config.Kind, year int, name string) (Ways, error) {
	w := Ways{
		Year:      year,
		AmountKr:  m.FeeFor(kind, year),
		Reference: m.Reference(name),
		Bankgiro:  strings.TrimSpace(m.Bankgiro),
	}
	if !m.TakesSwish() {
		return w, nil
	}
	link, err := swishLink(m.SwishNumber(), w.AmountKr, w.Reference)
	if err != nil {
		return w, err
	}
	png, err := qrPNG(link)
	if err != nil {
		return w, err
	}
	w.Swish = Swish{Number: strings.TrimSpace(m.Swish), QR: png, Link: link}
	return w, nil
}

// swishBase is the address the Swish app answers to. Everything after it is
// built by hand rather than with net/url, because the exact spelling matters
// and net/url will not produce it: url.Values sorts its keys alphabetically
// and encodes a space as "+", and this wants the parameters in Swish's own
// order with spaces as %20.
const swishBase = "https://app.swish.nu/1/p/sw/"

// swishLink builds the address a Swish QR code carries.
//
// This is Swish's own format, confirmed against the generator behind
// swish.nu: a payee, an amount in kronor, the currency, the message, which
// fields the payer may still change, and where the link came from.
//
//	https://app.swish.nu/1/p/sw/?sw=1231231231&amt=250&cur=SEK&msg=Medlemsavgift%20Åsa&edit=amt,msg&src=qr
//
// It is emphatically not the JSON that Swish's own HTTP API accepts. That
// JSON is what you send them to have a picture drawn for you; putting it
// inside a QR code yourself produces a code the Swish app cannot read, which
// is exactly what this used to do.
//
// Amount and message are left editable on purpose — somebody paying for two,
// or wanting to add a note, should not be stopped by a QR code — while the
// number they are paying is not theirs to edit.
func swishLink(number string, amountKr int, message string) (string, error) {
	if number == "" {
		return "", fmt.Errorf("no Swish number")
	}
	var b strings.Builder
	b.WriteString(swishBase)
	b.WriteString("?sw=")
	b.WriteString(number)
	if amountKr > 0 {
		// The currency rides with the amount and is left out without one,
		// which is what their generator does.
		b.WriteString("&amt=")
		b.WriteString(strconv.Itoa(amountKr))
		b.WriteString("&cur=SEK")
	}
	if msg := trim(message, 50); msg != "" {
		b.WriteString("&msg=")
		b.WriteString(escape(msg))
	}
	b.WriteString("&edit=amt,msg&src=qr")
	return b.String(), nil
}

// escape percent-encodes one query value exactly as Swish's own generator
// does. Theirs is a browser, so the set it leaves alone is JavaScript's
// encodeURIComponent set — the unreserved characters plus !~*'() — and a
// space becomes %20, an Å becomes %C3%85 and an ampersand %26.
//
// Matching them character for character is not fussiness. It is what lets the
// test next to this compare the two strings for equality and so notice if
// Swish ever changes the format under us. A stricter encoder would decode to
// the same thing in any correct parser, but it would also make that test
// impossible to write.
func escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			strings.IndexByte("-_.!~*'()", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0f])
	}
	return b.String()
}

// qrPNG encodes a payload as a data: URI.
//
// Medium error correction: a QR code on a screen is not a QR code on a lamp
// post, and the lower the correction the fewer modules, which keeps it
// readable on a small screen without growing the image.
func qrPNG(payload string) (string, error) {
	code, err := qr.Encode(payload, qr.M)
	if err != nil {
		return "", fmt.Errorf("encode the Swish QR code: %w", err)
	}
	var buf bytes.Buffer
	buf.WriteString("data:image/png;base64,")
	enc := base64.NewEncoder(base64.StdEncoding, &buf)
	if _, err := enc.Write(code.PNG()); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// trim shortens a message to what Swish accepts, on a rune boundary so that
// an å is never cut in half.
func trim(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return strings.TrimSpace(string(r[:max]))
}
