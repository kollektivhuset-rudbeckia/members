package payment

import (
	"encoding/base64"
	"encoding/json"
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

	var payload struct {
		Version int `json:"version"`
		Payee   struct {
			Value string `json:"value"`
		} `json:"payee"`
		Amount struct {
			Value    float64 `json:"value"`
			Editable bool    `json:"editable"`
		} `json:"amount"`
		Message struct {
			Value    string `json:"value"`
			Editable bool   `json:"editable"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(w.Swish.Payload), &payload); err != nil {
		t.Fatalf("the QR payload is not the JSON Swish expects: %v", err)
	}
	if payload.Version != 1 {
		t.Errorf("version: got %d, want 1", payload.Version)
	}
	// The digits alone, whatever spacing the configuration used.
	if payload.Payee.Value != "1234567890" {
		t.Errorf("payee: got %q", payload.Payee.Value)
	}
	if payload.Amount.Value != 250 {
		t.Errorf("amount: got %v, want 250", payload.Amount.Value)
	}
	if payload.Message.Value != "Medlemsavgift Anna Andersson" {
		t.Errorf("message: got %q", payload.Message.Value)
	}
	// Somebody paying for two, or wanting to add a note, must not be stopped
	// by a QR code.
	if !payload.Amount.Editable || !payload.Message.Editable {
		t.Error("the amount and message should stay editable in the app")
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
	var payload struct {
		Message struct {
			Value string `json:"value"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(w.Swish.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(payload.Message.Value)); n > 50 {
		t.Errorf("message is %d runes, want at most 50", n)
	}
	if strings.ContainsRune(payload.Message.Value, '�') {
		t.Error("a character was cut in half")
	}
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
