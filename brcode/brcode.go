// Package brcode builds the EMV® QR payloads ("Pix Copia e Cola") of the
// Pix Automático authorization journeys: Journey 2 (recurrence only),
// Journey 3 (immediate payment plus recurrence) and Journey 4 (charge with
// due date plus recurrence). The recurrence travels in template 80 as the
// URL of its payload location; the payment, when present, in template 26.
package brcode

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// GUI is the Pix globally unique identifier.
const GUI = "br.gov.bcb.pix"

// Errors returned by this package.
var (
	ErrField = errors.New("brcode: invalid field")
	ErrCRC   = errors.New("brcode: CRC mismatch")
)

// Payload describes a Pix Automático QR code.
type Payload struct {
	MerchantName string // 59, up to 25 characters
	MerchantCity string // 60, up to 15 characters
	// RecURL is the recurrence location without scheme, e.g.
	// "pix.example.com/qr/v2/rec/2353c790...". Required.
	RecURL string
	// CobURL is the location of the payment (Journey 3: immediate cob;
	// Journey 4: cobv). Empty for Journey 2.
	CobURL string
	// Amount (54) is optional and informative, e.g. "39.90".
	Amount string
}

func tlv(id, v string) string { return fmt.Sprintf("%s%02d%s", id, len(v), v) }

// CRC16 is CRC-16/CCITT-FALSE (poly 0x1021, init 0xFFFF), the BR Code CRC.
func CRC16(s string) uint16 {
	crc := uint16(0xFFFF)
	for i := 0; i < len(s); i++ {
		crc ^= uint16(s[i]) << 8
		for b := 0; b < 8; b++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func ascii(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7E {
			return false
		}
	}
	return true
}

// Encode returns the payload string with its CRC.
func (p Payload) Encode() (string, error) {
	switch {
	case p.MerchantName == "" || utf8.RuneCountInString(p.MerchantName) > 25 || !ascii(p.MerchantName):
		return "", fmt.Errorf("%w: merchant name must be 1-25 ASCII characters", ErrField)
	case p.MerchantCity == "" || len(p.MerchantCity) > 15 || !ascii(p.MerchantCity):
		return "", fmt.Errorf("%w: merchant city must be 1-15 ASCII characters", ErrField)
	case p.RecURL == "" || len(p.RecURL) > 77 || strings.Contains(p.RecURL, "://"):
		return "", fmt.Errorf("%w: recurrence URL must be 1-77 characters without scheme", ErrField)
	case len(p.CobURL) > 77 || strings.Contains(p.CobURL, "://"):
		return "", fmt.Errorf("%w: payment URL must be at most 77 characters without scheme", ErrField)
	}
	mai := tlv("00", GUI)
	if p.CobURL != "" {
		mai += tlv("25", p.CobURL)
	}
	var b strings.Builder
	b.WriteString(tlv("00", "01"))
	if p.CobURL != "" {
		b.WriteString(tlv("01", "12")) // dynamic, single use
	}
	b.WriteString(tlv("26", mai))
	b.WriteString(tlv("52", "0000"))
	b.WriteString(tlv("53", "986"))
	if p.Amount != "" {
		b.WriteString(tlv("54", p.Amount))
	}
	b.WriteString(tlv("58", "BR"))
	b.WriteString(tlv("59", p.MerchantName))
	b.WriteString(tlv("60", p.MerchantCity))
	b.WriteString(tlv("62", tlv("05", "***")))
	b.WriteString(tlv("80", tlv("00", GUI)+tlv("25", p.RecURL)))
	b.WriteString("6304")
	s := b.String()
	return s + fmt.Sprintf("%04X", CRC16(s)), nil
}

// Field is one top-level TLV.
type Field struct{ ID, Value string }

// Parse splits a payload into top-level fields and checks its CRC.
func Parse(s string) ([]Field, error) {
	if len(s) < 8 || s[len(s)-8:len(s)-4] != "6304" {
		return nil, fmt.Errorf("%w: missing CRC field", ErrField)
	}
	if got := fmt.Sprintf("%04X", CRC16(s[:len(s)-4])); !strings.EqualFold(got, s[len(s)-4:]) {
		return nil, ErrCRC
	}
	var out []Field
	for i := 0; i < len(s); {
		if i+4 > len(s) {
			return nil, ErrField
		}
		var n int
		if _, err := fmt.Sscanf(s[i+2:i+4], "%02d", &n); err != nil || i+4+n > len(s) {
			return nil, ErrField
		}
		out = append(out, Field{s[i : i+2], s[i+4 : i+4+n]})
		i += 4 + n
	}
	return out, nil
}

// Journey reports which authorization journey a parsed payload offers:
// 2 without a payment location, 3 or 4 with one (the location's payload
// tells an immediate charge from one with a due date), and 0 if there is no
// recurrence.
func Journey(fields []Field) int {
	rec, cob := false, false
	for _, f := range fields {
		if f.ID == "80" && strings.Contains(f.Value, GUI) {
			rec = true
		}
		if f.ID == "26" && strings.Contains(f.Value, "25") && len(f.Value) > len(tlv("00", GUI)) {
			cob = true
		}
	}
	switch {
	case !rec:
		return 0
	case cob:
		return 3
	}
	return 2
}
