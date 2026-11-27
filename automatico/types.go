// Package automatico is a Pix Automático engine: recurrences and their
// authorization journeys, payment instructions (scheduling), settlement
// windows, post-due retries and the cancellation rules of the Pix
// regulation, on a simulated clock.
//
// It models both PSPs of the arrangement in one process — the receiver's PSP
// (which creates recurrences and schedules charges) and the payer's PSP
// (which shows the authorization, enforces the payer's maximum value and
// limit, and debits the account) — so a recurring-billing product can be
// developed and tested end to end without a PSP sandbox. Every rule it
// enforces is listed in the README with its source.
package automatico

import (
	"errors"
	"fmt"
	"time"

	"github.com/victorcondackdev/pix-automatico-go/calendar"
)

// Money is an amount in centavos.
type Money int64

// String formats as "R$ 1.234,56".
func (m Money) String() string {
	neg := m < 0
	if neg {
		m = -m
	}
	reais := int64(m) / 100
	s := fmt.Sprintf("%d", reais)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, '.')
		}
		out = append(out, c)
	}
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%sR$ %s,%02d", sign, out, int64(m)%100)
}

// Decimal formats as "1234.56", the API Pix wire format.
func (m Money) Decimal() string { return fmt.Sprintf("%d.%02d", int64(m)/100, int64(m)%100) }

// Journey is the authorization journey (Jornada) through which the payer
// grants the authorization.
type Journey int

// Authorization journeys.
const (
	// Journey1: the receiver's PSP sends a confirmation request (pain.009)
	// that the payer's PSP shows as a notification in the app.
	Journey1 Journey = 1
	// Journey2: the payer reads a QR code carrying only the recurrence.
	Journey2 Journey = 2
	// Journey3: the QR code carries an immediate payment plus the
	// recurrence; both must succeed, and the immediate payment is not part
	// of the recurrence.
	Journey3 Journey = 3
	// Journey4: the QR code of a charge with a due date carries the
	// recurrence, offered when that charge is paid or scheduled.
	Journey4 Journey = 4
)

// RetryPolicy is the recurrence's post-due retry policy.
type RetryPolicy string

// Retry policies (API Pix names).
const (
	NoRetries RetryPolicy = "NAO_PERMITE"
	// Retries3R7D allows new attempts on at most three dates within seven
	// calendar days after the due date.
	Retries3R7D RetryPolicy = "PERMITE_3R_7D"
)

// RecStatus is the status of a recurrence (API Pix names).
type RecStatus string

// Recurrence statuses.
const (
	RecCreated   RecStatus = "CRIADA"
	RecApproved  RecStatus = "APROVADA"
	RecRejected  RecStatus = "REJEITADA"
	RecExpired   RecStatus = "EXPIRADA"
	RecCancelled RecStatus = "CANCELADA"
)

// SolicStatus is the status of a confirmation request (Journey 1).
type SolicStatus string

// Confirmation request statuses.
const (
	SolicCreated   SolicStatus = "CRIADA"
	SolicAccepted  SolicStatus = "ACEITA"
	SolicRejected  SolicStatus = "REJEITADA"
	SolicExpired   SolicStatus = "EXPIRADA"
	SolicCancelled SolicStatus = "CANCELADA"
)

// ChargeStatus is the status of a recurring charge (cobr).
type ChargeStatus string

// Charge statuses.
const (
	ChargeActive    ChargeStatus = "ATIVA"     // scheduled at the payer's PSP
	ChargeSettled   ChargeStatus = "CONCLUIDA" // paid
	ChargeExpired   ChargeStatus = "EXPIRADA"  // no attempt left
	ChargeRejected  ChargeStatus = "REJEITADA" // refused at scheduling (e.g. above the payer's maximum)
	ChargeCancelled ChargeStatus = "CANCELADA"
)

// AttemptType is why a payment instruction was sent.
type AttemptType string

// Attempt types (the instruction purposes of pain.013).
const (
	Scheduled AttemptType = "AGND" // original scheduling, 2–10 days ahead
	Retry     AttemptType = "NTAG" // post-due retry
)

// AttemptStatus is the status of one settlement attempt.
type AttemptStatus string

// Attempt statuses.
const (
	AttemptScheduled AttemptStatus = "AGENDADA"
	AttemptSettled   AttemptStatus = "LIQUIDADA"
	AttemptFailed    AttemptStatus = "NAO_LIQUIDADA"
	AttemptCancelled AttemptStatus = "CANCELADA"
)

// Party identifies who performs a cancellation.
type Party string

// Parties.
const (
	Payer    Party = "PAGADOR"
	Receiver Party = "RECEBEDOR"
)

// Debtor is the person or company that owes the recurring charges.
type Debtor struct {
	Document string // CPF (11 digits) or CNPJ (14)
	Name     string
}

// Recurrence is a recurring-payment agreement and, once approved, the
// payer's authorization.
type Recurrence struct {
	ID          string
	Contract    string // vinculo.contrato
	Object      string // vinculo.objeto, e.g. "Plano Premium"
	Debtor      Debtor
	Receiver    string // receiver name shown to the payer
	Start       calendar.Date
	End         calendar.Date // zero means indefinite
	Periodicity calendar.Periodicity
	// FixedValue is the value of every charge; zero means variable.
	FixedValue Money
	// ReceiverFloor is the minimum the payer may set as maximum value
	// (valorMinimoRecebedor), variable recurrences only.
	ReceiverFloor       Money
	RetryPolicy         RetryPolicy
	AdjustToBusinessDay bool
	Journey             Journey
	Status              RecStatus
	PayerMax            Money // maximum value set by the payer; zero means none
	Revision            int   // incremented when the receiver revises the offer
	CreatedAt           time.Time
	UpdatedAt           time.Time
	CancelledBy         Party
	CancelReason        string
}

// Variable reports whether charges may differ cycle to cycle.
func (r *Recurrence) Variable() bool { return r.FixedValue == 0 }

// Solicitation is a Journey 1 confirmation request.
type Solicitation struct {
	ID       string
	RecID    string
	Revision int
	Expiry   time.Time
	Status   SolicStatus
	Created  time.Time
	Ended    time.Time
}

// Attempt is one settlement attempt of a charge.
type Attempt struct {
	Type   AttemptType
	Date   calendar.Date
	Status AttemptStatus
	Reason string
}

// Charge is one cycle's payment of a recurrence.
type Charge struct {
	TxID     string
	RecID    string
	Cycle    int
	DueDate  calendar.Date
	Amount   Money
	Status   ChargeStatus
	Reason   string
	Attempts []Attempt
	Created  time.Time
	Settled  time.Time
}

func (c *Charge) last() *Attempt {
	if len(c.Attempts) == 0 {
		return nil
	}
	return &c.Attempts[len(c.Attempts)-1]
}

// Error is a rule violation, with the API Pix error type it maps to.
type Error struct {
	Type   string // e.g. "RecOperacaoInvalida", "CobrOperacaoInvalida", "NaoEncontrado"
	Detail string
}

func (e *Error) Error() string { return "automatico: " + e.Type + ": " + e.Detail }

// Is lets errors.Is match on Type.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Type == e.Type && (t.Detail == "" || t.Detail == e.Detail)
}

// Sentinels for errors.Is.
var (
	ErrNotFound      = &Error{Type: "NaoEncontrado"}
	ErrRecInvalid    = &Error{Type: "RecOperacaoInvalida"}
	ErrSolicInvalid  = &Error{Type: "SolicRecOperacaoInvalida"}
	ErrChargeInvalid = &Error{Type: "CobrOperacaoInvalida"}
	ErrPaymentFailed = &Error{Type: "PagamentoNaoRealizado"}
	errInsufficient  = errors.New("saldo insuficiente")
	errLimit         = errors.New("limite do Pix Automático insuficiente")
)

func recErr(format string, a ...any) error {
	return &Error{Type: "RecOperacaoInvalida", Detail: fmt.Sprintf(format, a...)}
}
func solicErr(format string, a ...any) error {
	return &Error{Type: "SolicRecOperacaoInvalida", Detail: fmt.Sprintf(format, a...)}
}
func chargeErr(format string, a ...any) error {
	return &Error{Type: "CobrOperacaoInvalida", Detail: fmt.Sprintf(format, a...)}
}
func notFound(what, id string) error {
	return &Error{Type: "NaoEncontrado", Detail: what + " " + id + " não encontrada"}
}
