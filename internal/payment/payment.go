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
	"encoding/json"
	"fmt"
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
	// Payload is what the QR encodes, kept for the test that has to read it.
	Payload string
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
	payload, err := swishPayload(m.SwishNumber(), w.AmountKr, w.Reference)
	if err != nil {
		return w, err
	}
	png, err := qrPNG(payload)
	if err != nil {
		return w, err
	}
	w.Swish = Swish{Number: strings.TrimSpace(m.Swish), QR: png, Payload: payload}
	return w, nil
}

// swishPayload builds the JSON a Swish QR code carries.
//
// The shape is Swish's own: a payee, an amount and a message, each with a
// flag saying whether the person paying may change it. Amount and message are
// left editable on purpose — somebody paying for two, or wanting to add a
// note, should not be stopped by a QR code — while the number they are paying
// is not theirs to edit.
func swishPayload(number string, amountKr int, message string) (string, error) {
	if number == "" {
		return "", fmt.Errorf("no Swish number")
	}
	type value struct {
		Value    any  `json:"value"`
		Editable bool `json:"editable,omitempty"`
	}
	payload := struct {
		Version int   `json:"version"`
		Payee   value `json:"payee"`
		Amount  value `json:"amount"`
		Message value `json:"message"`
	}{
		Version: 1,
		Payee:   value{Value: number},
		Amount:  value{Value: amountKr, Editable: true},
		Message: value{Value: trim(message, 50), Editable: true},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
