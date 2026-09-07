package payment

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

func membership() config.Membership {
	return config.Membership{
		FeeKr:            300,
		FeesByYear:       map[int]int{2026: 250},
		Bankgiro:         "5752-5925",
		PaymentReference: "Medlemsavgift %s",
	}
}

func TestBankgiroAloneWhenThereIsNoSwish(t *testing.T) {
	w, err := For(membership(), config.KindVan, 2026, "Anna Andersson")
	if err != nil {
		t.Fatal(err)
	}
	if w.AmountKr != 250 {
		t.Errorf("amount: got %d, want the 2026 fee of 250", w.AmountKr)
	}
	if w.Bankgiro != "5752-5925" {
		t.Errorf("bankgiro: got %q", w.Bankgiro)
	}
	if w.Reference != "Medlemsavgift Anna Andersson" {
		t.Errorf("reference: got %q", w.Reference)
	}
	if w.Swish.Offered() {
		t.Error("Swish is offered with no number configured")
	}
}

// The Swish link format is not published anywhere the association can cite,
// so it was read off Swish's own generator rather than guessed. If it ever
// needs re-checking, the recipe is:
//
//	curl -sS https://api.swish.nu/qr/v2/prefilled \
//	  -H 'content-type: application/json' -H 'origin: https://www.swish.nu' \
//	  --data-raw '{"size":600,"border":1,"payee":"1231231231","color":false,
//	     "amount":{"value":250,"editable":true},
//	     "message":{"value":"Medlemsavgift Åsa","editable":true}}' -o qr.png
//	zbarimg --raw qr.png
//
// and the string that comes out should be the one this package builds for the
// same three values. That JSON is the input to *their* drawing service; it is
// not what belongs inside a QR code, and sending it there was the bug this
// format replaced.

func TestSwishCarriesTheNumberAmountAndReference(t *testing.T) {
	m := membership()
	m.Swish = "123 456 78 90"

	w, err := For(m, config.KindBo, 2026, "Anna Andersson")
	if err != nil {
		t.Fatal(err)
	}
	if !w.Swish.Offered() {
		t.Fatal("Swish is not offered")
	}
	if w.Swish.Number != "123 456 78 90" {
		t.Errorf("the number should read as a person writes it: %q", w.Swish.Number)
	}

	// Spelled out in full rather than picked apart, because the whole point of
	// this format is that it matches Swish's byte for byte. A QR code that is
	// merely nearly right is a QR code that makes the app show an error, which
	// is exactly what the hand-rolled JSON payload this replaced did.
	const want = "https://app.swish.nu/1/p/sw/" +
		"?sw=1234567890&amt=250&cur=SEK&msg=Medlemsavgift%20Anna%20Andersson" +
		"&edit=amt,msg&src=qr"
	if w.Swish.Link != want {
		t.Errorf("link:\n got  %s\n want %s", w.Swish.Link, want)
	}
}

// The QR must carry the app's own address and nothing else. Swish's HTTP API
// takes a JSON body describing a payment, and it is an easy and costly
// mistake to think that JSON is also what belongs inside the code: the app
// cannot read it, and the only symptom is an error message on somebody's
// phone at the moment they were trying to pay us.
func TestTheQRIsALinkAndNotJSON(t *testing.T) {
	m := membership()
	m.Swish = "1234567890"
	w, err := For(m, config.KindBo, 2026, "Anna")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(w.Swish.Link, "https://app.swish.nu/") {
		t.Errorf("the QR should carry an app.swish.nu link: %q", w.Swish.Link)
	}
	if strings.HasPrefix(strings.TrimSpace(w.Swish.Link), "{") {
		t.Error("the QR carries JSON again; the Swish app cannot read that")
	}
}

// Percent-encoding, matching the browser's encodeURIComponent, so that a
// Swedish name survives the trip and an ampersand cannot end the parameter
// early.
func TestTheMessageIsEncodedLikeTheBrowserDoes(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Medlemsavgift Åsa Öberg", "Medlemsavgift%20%C3%85sa%20%C3%96berg"},
		{"Anna & Per, lgh 42", "Anna%20%26%20Per%2C%20lgh%2042"},
		{"Jean-Luc O'Brien", "Jean-Luc%20O'Brien"},
		{"Zola/Müller +1", "Zola%2FM%C3%BCller%20%2B1"},
		{"100% (2026)", "100%25%20(2026)"},
	} {
		if got := escape(tc.in); got != tc.want {
			t.Errorf("escape(%q):\n got  %s\n want %s", tc.in, got, tc.want)
		}
	}
}

// Without an amount there is no currency either, which is what their
// generator does and what the app expects.
func TestNoAmountMeansNoCurrency(t *testing.T) {
	link, err := swishLink("1234567890", 0, "Medlemsavgift")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(link, "cur=") || strings.Contains(link, "amt=") {
		t.Errorf("a link with no amount should carry neither amt nor cur: %s", link)
	}
}

// The QR goes into the page as a data: URI, which is what lets the page need
// no second request and no external host — the Content-Security-Policy allows
// data: images and nothing else.
func TestTheQRIsAnInlinePNG(t *testing.T) {
	m := membership()
	m.Swish = "1234567890"
	w, err := For(m, config.KindBo, 2026, "Anna")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(w.Swish.QR, prefix) {
		t.Fatalf("not a data URI: %.40q", w.Swish.QR)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(w.Swish.QR, prefix))
	if err != nil {
		t.Fatalf("the payload is not base64: %v", err)
	}
	if len(raw) < 100 {
		t.Errorf("the image is suspiciously small: %d bytes", len(raw))
	}
	if string(raw[1:4]) != "PNG" {
		t.Errorf("not a PNG: % x", raw[:8])
	}
}

// Swish caps the message length. A name cut mid-rune would render as a
// replacement character in somebody's banking app.
func TestALongReferenceIsTrimmedOnARuneBoundary(t *testing.T) {
	m := membership()
	m.Swish = "1234567890"
	long := "Anna-Karin Åström-Öbergsson från Kollektivhuset Rudbeckia i Rosendal"

	w, err := For(m, config.KindBo, 2026, long)
	if err != nil {
		t.Fatal(err)
	}
	msg := messageOf(t, w.Swish.Link)
	if n := len([]rune(msg)); n > 50 {
		t.Errorf("message is %d runes, want at most 50", n)
	}
	if strings.ContainsRune(msg, '\uFFFD') {
		t.Errorf("a character was cut in half: %q", msg)
	}
}

// messageOf reads the msg parameter back out of a link, decoded.
func messageOf(t *testing.T, link string) string {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("the link does not parse as a URL: %v", err)
	}
	return u.Query().Get("msg")
}

func TestTheFeeFollowsTheYear(t *testing.T) {
	m := membership()
	for _, tc := range []struct{ year, want int }{{2026, 250}, {2027, 300}, {2025, 300}} {
		w, err := For(m, config.KindBo, tc.year, "Anna")
		if err != nil {
			t.Fatal(err)
		}
		if w.AmountKr != tc.want {
			t.Errorf("%d: got %d kr, want %d", tc.year, w.AmountKr, tc.want)
		}
	}
}
