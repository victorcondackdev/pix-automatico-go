package brcode

import (
	"errors"
	"testing"
)

// A Journey 2 payload published in a PSP's API Pix documentation.
const published = "00020126180014br.gov.bcb.pix5204000053039865802BR5913Fulano de Tal6008BRASILIA62070503***80800014br.gov.bcb.pix2558pix.example.com/qr/v2/rec/2353c790eefb11eaadc10242ac120002630462C9"

func TestPublishedJourney2Payload(t *testing.T) {
	got, err := Payload{MerchantName: "Fulano de Tal", MerchantCity: "BRASILIA",
		RecURL: "pix.example.com/qr/v2/rec/2353c790eefb11eaadc10242ac120002"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if got != published {
		t.Fatalf("\n got %s\nwant %s", got, published)
	}
	f, err := Parse(published)
	if err != nil || Journey(f) != 2 {
		t.Fatalf("parse: %v journey %d", err, Journey(f))
	}
}

func TestJourney3Payload(t *testing.T) {
	s, err := Payload{MerchantName: "Academia Forte", MerchantCity: "RIO DE JANEIRO", Amount: "99.90",
		CobURL: "pix.example.com/qr/v2/cob/1a2b3c", RecURL: "pix.example.com/qr/v2/rec/9f8e7d"}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	if Journey(f) != 3 {
		t.Fatalf("journey %d for %s", Journey(f), s)
	}
	ids := ""
	for _, x := range f {
		ids += x.ID + " "
	}
	if ids != "00 01 26 52 53 54 58 59 60 62 80 63 " {
		t.Fatalf("field order %s", ids)
	}
}

func TestValidationAndCRC(t *testing.T) {
	if _, err := (Payload{MerchantName: "x", MerchantCity: "y", RecURL: "https://a/b"}).Encode(); !errors.Is(err, ErrField) {
		t.Fatal("scheme accepted")
	}
	if _, err := (Payload{MerchantName: "Açaí", MerchantCity: "y", RecURL: "a/b"}).Encode(); !errors.Is(err, ErrField) {
		t.Fatal("non-ASCII accepted")
	}
	bad := published[:len(published)-1] + "0"
	if _, err := Parse(bad); !errors.Is(err, ErrCRC) {
		t.Fatalf("bad CRC: %v", err)
	}
	if CRC16("123456789") != 0x29B1 {
		t.Fatal("CRC-16/CCITT-FALSE check value")
	}
}
