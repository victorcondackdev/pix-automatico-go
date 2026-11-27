package automatico

import (
	"errors"
	"strings"
	"testing"

	"github.com/victorcondackdev/pix-automatico-go/calendar"
)

const cpf = "12345678909"

var d = calendar.MustParse

func newEngine(t *testing.T) *Engine {
	t.Helper()
	e := New(Config{ISPB: "12345678", Start: d("2026-11-27").At(10, 0, calendar.BRT)})
	e.SetAccount(cpf, 100000, 0)
	return e
}

func rec(t *testing.T, e *Engine, mod func(*RecRequest)) *Recurrence {
	t.Helper()
	req := RecRequest{Contract: "CTR-1", Object: "Plano Premium", Debtor: Debtor{cpf, "Fulano"}, Receiver: "Streaming SA",
		Start: d("2026-12-10"), Periodicity: calendar.Monthly, FixedValue: 3990, Journey: Journey2}
	if mod != nil {
		mod(&req)
	}
	r, err := e.CreateRecurrence(req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ok(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantErr(t *testing.T, err error, typ *Error, contains string) {
	t.Helper()
	if !errors.Is(err, typ) {
		t.Fatalf("want %s, got %v", typ.Type, err)
	}
	if contains != "" && !strings.Contains(err.Error(), contains) {
		t.Fatalf("error %q does not mention %q", err, contains)
	}
}

func events(e *Engine) string {
	var b strings.Builder
	for _, ev := range e.Events() {
		b.WriteString(ev.String())
		b.WriteByte('\n')
	}
	return b.String()
}

func TestHappyPathJourney2(t *testing.T) {
	e := newEngine(t)
	r := rec(t, e, nil)
	if !strings.HasPrefix(r.ID, "RR1234567820261127") || len(r.ID) != 29 {
		t.Fatalf("id %s", r.ID)
	}
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-11-30"), 9, 0)
	c, err := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	ok(t, err)
	if c.Status != ChargeActive || c.Cycle != 0 {
		t.Fatalf("charge %+v", c)
	}
	e.AdvanceTo(d("2026-12-10"), 9, 0)
	got, _ := e.Charge(c.TxID)
	if got.Status != ChargeSettled || got.Settled.Hour() != 0 {
		t.Fatalf("settled %+v", got)
	}
	if e.Balance(cpf) != 100000-3990 {
		t.Fatalf("balance %d", e.Balance(cpf))
	}
	// Next cycle: 2027-01-10.
	e.AdvanceTo(d("2026-12-31"), 9, 0)
	c2, err := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2027-01-10"), Amount: 3990})
	ok(t, err)
	if c2.Cycle != 1 {
		t.Fatalf("cycle %d", c2.Cycle)
	}
}

func TestSchedulingWindowAndCycles(t *testing.T) {
	e := newEngine(t)
	r := rec(t, e, nil)
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	_, err := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	wantErr(t, err, ErrChargeInvalid, "faltam 13")
	e.AdvanceTo(d("2026-12-09"), 9, 0)
	_, err = e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	wantErr(t, err, ErrChargeInvalid, "faltam 1")
	e2 := newEngine(t)
	r2 := rec(t, e2, nil)
	ok(t, e2.Approve(r2.ID, ApproveOptions{}))
	e2.AdvanceTo(d("2026-12-01"), 9, 0)
	_, err = e2.ScheduleCharge(ChargeRequest{RecID: r2.ID, DueDate: d("2026-12-10"), Amount: 4000})
	wantErr(t, err, ErrChargeInvalid, "valor fixo")
	_, err = e2.ScheduleCharge(ChargeRequest{RecID: r2.ID, DueDate: d("2026-12-08"), Amount: 3990})
	wantErr(t, err, ErrChargeInvalid, "vigência")
	_, err = e2.ScheduleCharge(ChargeRequest{RecID: r2.ID, DueDate: d("2026-12-10"), Amount: 3990})
	ok(t, err)
	_, err = e2.ScheduleCharge(ChargeRequest{RecID: r2.ID, DueDate: d("2026-12-11"), Amount: 3990})
	wantErr(t, err, ErrChargeInvalid, "ciclo 0")
}

func TestSecondWindowAndNotification(t *testing.T) {
	e := newEngine(t)
	e.SetAccount(cpf, 1000, 0)
	r := rec(t, e, nil)
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e.AdvanceTo(d("2026-12-10"), 12, 0)
	if !strings.Contains(events(e), "cobr.janela1.falhou") || !strings.Contains(events(e), "nova tentativa às 18h") {
		t.Fatalf("window 1 failure not notified:\n%s", events(e))
	}
	e.Deposit(cpf, 5000)
	e.AdvanceTo(d("2026-12-10"), 22, 0)
	got, _ := e.Charge(c.TxID)
	if got.Status != ChargeSettled || got.Settled.Hour() != 18 {
		t.Fatalf("not settled in window 2: %+v", got)
	}
}

func TestNoRetriesExpires(t *testing.T) {
	e := newEngine(t)
	e.SetAccount(cpf, 0, 0)
	r := rec(t, e, nil)
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e.AdvanceTo(d("2026-12-11"), 9, 0)
	got, _ := e.Charge(c.TxID)
	if got.Status != ChargeExpired {
		t.Fatalf("status %s", got.Status)
	}
	wantErr(t, e.RequestRetry(c.TxID, d("2026-12-12")), ErrChargeInvalid, "não permite")
}

func TestRetries3R7D(t *testing.T) {
	e := newEngine(t)
	e.SetAccount(cpf, 0, 0)
	r := rec(t, e, func(q *RecRequest) { q.RetryPolicy = Retries3R7D })
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	wantErr(t, e.RequestRetry(c.TxID, d("2026-12-12")), ErrChargeInvalid, "após tentativa")
	e.AdvanceTo(d("2026-12-10"), 22, 0)
	wantErr(t, e.RequestRetry(c.TxID, d("2026-12-10")), ErrChargeInvalid, "após o vencimento")
	wantErr(t, e.RequestRetry(c.TxID, d("2026-12-18")), ErrChargeInvalid, "7 dias")
	for i, day := range []string{"2026-12-11", "2026-12-14", "2026-12-17"} {
		ok(t, e.RequestRetry(c.TxID, d(day)))
		if i == 0 {
			wantErr(t, e.RequestRetry(c.TxID, d("2026-12-12")), ErrChargeInvalid, "após tentativa")
		}
		e.AdvanceTo(d(day), 22, 0)
	}
	got, _ := e.Charge(c.TxID)
	if got.Status != ChargeExpired || len(got.Attempts) != 4 {
		t.Fatalf("after 3 retries: %s, %d attempts", got.Status, len(got.Attempts))
	}
	for _, a := range got.Attempts[1:] {
		if a.Type != Retry || a.Status != AttemptFailed {
			t.Fatalf("attempt %+v", a)
		}
	}
	// The same txid is kept for every retry.
	if strings.Count(events(e), c.TxID+" NTAG") != 3 {
		t.Fatalf("retries should keep the txid:\n%s", events(e))
	}
}

func TestRetrySucceeds(t *testing.T) {
	e := newEngine(t)
	e.SetAccount(cpf, 0, 0)
	r := rec(t, e, func(q *RecRequest) { q.RetryPolicy = Retries3R7D })
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e.AdvanceTo(d("2026-12-10"), 23, 0)
	ok(t, e.RequestRetry(c.TxID, d("2026-12-15")))
	e.Deposit(cpf, 3990)
	e.AdvanceTo(d("2026-12-15"), 9, 0)
	got, _ := e.Charge(c.TxID)
	if got.Status != ChargeSettled {
		t.Fatalf("retry status %s", got.Status)
	}
	// Past D+7 without a retry, an active failed charge expires by itself.
	e2 := newEngine(t)
	e2.SetAccount(cpf, 0, 0)
	r2 := rec(t, e2, func(q *RecRequest) { q.RetryPolicy = Retries3R7D })
	ok(t, e2.Approve(r2.ID, ApproveOptions{}))
	e2.AdvanceTo(d("2026-12-01"), 9, 0)
	c2, _ := e2.ScheduleCharge(ChargeRequest{RecID: r2.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e2.AdvanceTo(d("2026-12-17"), 23, 59)
	if g, _ := e2.Charge(c2.TxID); g.Status != ChargeActive {
		t.Fatalf("still retryable on D+7: %s", g.Status)
	}
	e2.AdvanceTo(d("2026-12-18"), 0, 1)
	if g, _ := e2.Charge(c2.TxID); g.Status != ChargeExpired {
		t.Fatalf("after D+7: %s", g.Status)
	}
}

func TestMaxValueAndFloor(t *testing.T) {
	e := newEngine(t)
	r := rec(t, e, func(q *RecRequest) { q.FixedValue = 0; q.ReceiverFloor = 5000 })
	wantErr(t, e.Approve(r.ID, ApproveOptions{MaxValue: 4000}), ErrRecInvalid, "piso")
	ok(t, e.Approve(r.ID, ApproveOptions{MaxValue: 10000}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, err := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 15000})
	ok(t, err)
	if c.Status != ChargeRejected {
		t.Fatalf("status %s", c.Status)
	}
	if !strings.Contains(events(e), "acima do seu valor máximo") {
		t.Fatal("payer not notified")
	}
	if rr, _ := e.Recurrence(r.ID); rr.Status != RecApproved {
		t.Fatal("authorization must stay active")
	}
	ok(t, e.SetMaxValue(r.ID, 20000))
	c, err = e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 15000})
	ok(t, err)
	if c.Status != ChargeActive {
		t.Fatalf("after raising max: %s", c.Status)
	}
	fixed := rec(t, e, func(q *RecRequest) { q.Contract = "CTR-2" })
	wantErr(t, e.Approve(fixed.ID, ApproveOptions{MaxValue: 10000}), ErrRecInvalid, "variável")
}

func TestDailyLimit(t *testing.T) {
	e := newEngine(t)
	e.SetAccount(cpf, 100000, 3000)
	r := rec(t, e, nil)
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	if c.Status != ChargeActive || !strings.Contains(events(e), "acima do seu limite diário") {
		t.Fatalf("scheduling above the limit is allowed with a warning:\n%s", events(e))
	}
	e.AdvanceTo(d("2026-12-11"), 0, 0)
	got, _ := e.Charge(c.TxID)
	if got.Status != ChargeExpired || got.Attempts[0].Reason != errLimit.Error() {
		t.Fatalf("%+v", got)
	}
}

func TestJourney1Solicitation(t *testing.T) {
	e := newEngine(t)
	r := rec(t, e, func(q *RecRequest) { q.Journey = Journey1 })
	wantErr(t, e.Approve(r.ID, ApproveOptions{}), ErrRecInvalid, "solicitação")
	now := e.Now()
	_, err := e.RequestConfirmation(r.ID, now.AddDate(0, 0, 31))
	wantErr(t, err, ErrSolicInvalid, "30 dias")
	s, err := e.RequestConfirmation(r.ID, now.AddDate(0, 0, 3))
	ok(t, err)
	_, err = e.RequestConfirmation(r.ID, now.AddDate(0, 0, 3))
	wantErr(t, err, ErrSolicInvalid, "já existe")
	wantErr(t, e.ReviseStart(r.ID, d("2026-12-15")), ErrRecInvalid, "ERSL")
	e.AdvanceTo(d("2026-12-01"), 0, 0)
	if got, _ := e.Solicitation(s.ID); got.Status != SolicExpired {
		t.Fatalf("solicitation %s", got.Status)
	}
	_, err = e.RequestConfirmation(r.ID, e.Now().AddDate(0, 0, 3))
	wantErr(t, err, ErrSolicInvalid, "idêntica")
	ok(t, e.ReviseStart(r.ID, d("2026-12-15")))
	s2, err := e.RequestConfirmation(r.ID, e.Now().AddDate(0, 0, 3))
	ok(t, err)
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	if got, _ := e.Solicitation(s2.ID); got.Status != SolicAccepted {
		t.Fatalf("solicitation after approval %s", got.Status)
	}
	other := rec(t, e, func(q *RecRequest) { q.Journey = Journey2; q.Contract = "X" })
	_, err = e.RequestConfirmation(other.ID, e.Now().AddDate(0, 0, 1))
	wantErr(t, err, ErrSolicInvalid, "jornada 1")
}

func TestJourney3NeedsImmediatePayment(t *testing.T) {
	e := newEngine(t)
	e.SetAccount(cpf, 1000, 0)
	r := rec(t, e, func(q *RecRequest) { q.Journey = Journey3 })
	wantErr(t, e.Approve(r.ID, ApproveOptions{}), ErrRecInvalid, "pagamento imediato")
	wantErr(t, e.Approve(r.ID, ApproveOptions{ImmediatePayment: 3990}), ErrPaymentFailed, "saldo insuficiente")
	if rr, _ := e.Recurrence(r.ID); rr.Status != RecCreated || e.Balance(cpf) != 1000 {
		t.Fatal("failed immediate payment must leave nothing behind")
	}
	e.Deposit(cpf, 3990)
	ok(t, e.Approve(r.ID, ApproveOptions{ImmediatePayment: 3990}))
	if e.Balance(cpf) != 1000 {
		t.Fatalf("balance %d", e.Balance(cpf))
	}
}

func TestCancelAuthorizationCutoff(t *testing.T) {
	for _, tc := range []struct {
		hh, mm   int
		survives bool
	}{{21, 59, false}, {22, 0, true}} {
		e := newEngine(t)
		r := rec(t, e, func(q *RecRequest) { q.Periodicity = calendar.Weekly; q.Start = d("2026-12-04") })
		ok(t, e.Approve(r.ID, ApproveOptions{}))
		e.AdvanceTo(d("2026-12-01"), 9, 0)
		c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-04"), Amount: 3990})
		e.AdvanceTo(d("2026-12-03"), tc.hh, tc.mm)
		ok(t, e.CancelAuthorization(r.ID, Payer, "não quero mais"))
		e.AdvanceTo(d("2026-12-05"), 0, 0)
		got, _ := e.Charge(c.TxID)
		if (got.Status == ChargeSettled) != tc.survives {
			t.Fatalf("cancel at %02d:%02d: charge %s", tc.hh, tc.mm, got.Status)
		}
	}
	e := newEngine(t)
	r := rec(t, e, nil)
	wantErr(t, e.CancelAuthorization(r.ID, Payer, "x"), ErrRecInvalid, "rejeita")
	ok(t, e.CancelAuthorization(r.ID, Receiver, "contrato desfeito"))
	wantErr(t, e.Approve(r.ID, ApproveOptions{}), ErrRecInvalid, "CANCELADA")
}

func TestCancelCharge(t *testing.T) {
	e := newEngine(t)
	r := rec(t, e, nil)
	ok(t, e.Approve(r.ID, ApproveOptions{}))
	e.AdvanceTo(d("2026-12-01"), 9, 0)
	c, _ := e.ScheduleCharge(ChargeRequest{RecID: r.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e.AdvanceTo(d("2026-12-10"), 0, 0)
	if got, _ := e.Charge(c.TxID); got.Status != ChargeSettled {
		t.Fatal("setup")
	}
	e2 := newEngine(t)
	r2 := rec(t, e2, nil)
	ok(t, e2.Approve(r2.ID, ApproveOptions{}))
	e2.AdvanceTo(d("2026-12-01"), 9, 0)
	c2, _ := e2.ScheduleCharge(ChargeRequest{RecID: r2.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e2.AdvanceTo(d("2026-12-09"), 23, 59)
	ok(t, e2.CancelCharge(c2.TxID, Payer))
	if rr, _ := e2.Recurrence(r2.ID); rr.Status != RecApproved {
		t.Fatal("cancelling a charge keeps the authorization")
	}
	e3 := newEngine(t)
	r3 := rec(t, e3, nil)
	ok(t, e3.Approve(r3.ID, ApproveOptions{}))
	e3.AdvanceTo(d("2026-12-01"), 9, 0)
	c3, _ := e3.ScheduleCharge(ChargeRequest{RecID: r3.ID, DueDate: d("2026-12-10"), Amount: 3990})
	e3.SetAccount(cpf, 0, 0)
	e3.AdvanceTo(d("2026-12-10"), 0, 30)
	wantErr(t, e3.CancelCharge(c3.TxID, Receiver), ErrChargeInvalid, "véspera")
}

func TestCreateValidation(t *testing.T) {
	e := newEngine(t)
	cases := []struct {
		mod  func(*RecRequest)
		want string
	}{
		{func(q *RecRequest) { q.Contract = "" }, "contrato"},
		{func(q *RecRequest) { q.Debtor.Document = "123" }, "CPF"},
		{func(q *RecRequest) { q.Periodicity = "DIARIA" }, "periodicidade"},
		{func(q *RecRequest) { q.Start = d("2026-11-01") }, "dataInicial"},
		{func(q *RecRequest) { q.End = d("2026-12-01") }, "dataFinal"},
		{func(q *RecRequest) { q.ReceiverFloor = 100 }, "variável"},
		{func(q *RecRequest) { q.Journey = 5 }, "jornada"},
		{func(q *RecRequest) { q.RetryPolicy = "SEMPRE" }, "politicaRetentativa"},
	}
	for _, c := range cases {
		req := RecRequest{Contract: "C", Debtor: Debtor{cpf, "F"}, Start: d("2026-12-10"), Periodicity: calendar.Monthly, FixedValue: 100, Journey: Journey2}
		c.mod(&req)
		_, err := e.CreateRecurrence(req)
		wantErr(t, err, ErrRecInvalid, c.want)
	}
	rec(t, e, nil)
	_, err := e.CreateRecurrence(RecRequest{Contract: "CTR-1", Debtor: Debtor{cpf, "F"}, Start: d("2026-12-10"), Periodicity: calendar.Monthly, Journey: Journey2})
	wantErr(t, err, ErrRecInvalid, "já existe")
	_, err = e.Recurrence("nope")
	wantErr(t, err, ErrNotFound, "")
}

func TestMoney(t *testing.T) {
	for m, want := range map[Money]string{0: "R$ 0,00", 3990: "R$ 39,90", 123456789: "R$ 1.234.567,89", -5: "-R$ 0,05"} {
		if m.String() != want {
			t.Errorf("%d = %s", m, m.String())
		}
	}
	if Money(3990).Decimal() != "39.90" {
		t.Error("decimal")
	}
}
