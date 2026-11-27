package automatico

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/victorcondackdev/pix-automatico-go/calendar"
)

// Rule constants from the Pix regulation and the Pix Automático guides.
const (
	// MinScheduleDays and MaxScheduleDays bound how many calendar days
	// before the due date a charge may be scheduled.
	MinScheduleDays = 2
	MaxScheduleDays = 10
	// RetryDays is how many days after the due date retries may happen.
	RetryDays = 7
	// MaxRetryDates is how many distinct retry dates are allowed.
	MaxRetryDates = 3
	// MaxSolicitationDays is the longest a confirmation request may live,
	// and how long an unconfirmed identical offer blocks a new one.
	MaxSolicitationDays = 30
	// LateCancelHour: an authorization cancelled at or after 22:00 keeps
	// the next day's payments as well.
	LateCancelHour = 22
)

// Settlement windows: the payer's PSP must try in the first window and, if
// that fails, again in the second on the same day.
var (
	Window1 = [2]int{0, 8}   // 00:00–08:00
	Window2 = [2]int{18, 21} // 18:00–21:00
)

// Event is something the engine did, for webhooks, payer notifications and
// audit. The log is deterministic for a given sequence of calls.
type Event struct {
	At     time.Time
	Kind   string // e.g. "rec.aprovada", "cobr.liquidada", "notificacao.pagador"
	Ref    string // recurrence, solicitation or charge id
	Detail string
}

func (e Event) String() string {
	s := e.At.In(calendar.BRT).Format("2006-01-02 15:04") + " " + e.Kind + " " + e.Ref
	if e.Detail != "" {
		s += " " + e.Detail
	}
	return s
}

// Account is the payer's transactional account at the payer's PSP.
type Account struct {
	Balance Money
	// DailyLimit is the Pix Automático limit per day, separate from the
	// Pix limit; zero means no limit.
	DailyLimit Money
	used       map[calendar.Date]Money
}

// Config configures an Engine.
type Config struct {
	ISPB     string // receiver PSP ISPB, 8 digits, used in identifiers
	Start    time.Time
	Holidays calendar.Holidays // nil means calendar.National{}
}

// Engine runs Pix Automático on a simulated clock. It is safe for
// concurrent use; all operations are serialized.
type Engine struct {
	mu       sync.Mutex
	ispb     string
	now      time.Time
	holidays calendar.Holidays
	seq      int

	recs     map[string]*Recurrence
	solics   map[string]*Solicitation
	charges  map[string]*Charge
	accounts map[string]*Account
	events   []Event
	// window state: charge txid -> windows tried for the current attempt
	tried map[string]int
}

// New returns an engine whose clock starts at cfg.Start.
func New(cfg Config) *Engine {
	h := cfg.Holidays
	if h == nil {
		h = calendar.National{}
	}
	ispb := cfg.ISPB
	if ispb == "" {
		ispb = "00000000"
	}
	return &Engine{ispb: ispb, now: cfg.Start.In(calendar.BRT), holidays: h,
		recs: map[string]*Recurrence{}, solics: map[string]*Solicitation{}, charges: map[string]*Charge{},
		accounts: map[string]*Account{}, tried: map[string]int{}}
}

// Now returns the engine clock.
func (e *Engine) Now() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.now
}

func (e *Engine) today() calendar.Date { return calendar.FromTime(e.now) }

func (e *Engine) emit(kind, ref, format string, a ...any) {
	e.events = append(e.events, Event{At: e.now, Kind: kind, Ref: ref, Detail: fmt.Sprintf(format, a...)})
}

// Events returns the event log.
func (e *Engine) Events() []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Event(nil), e.events...)
}

func (e *Engine) id(prefix string) string {
	e.seq++
	d := e.today()
	return fmt.Sprintf("%s%s%04d%02d%02d%011d", prefix, e.ispb, d.Year, int(d.Month), d.Day, e.seq)
}

// SetAccount creates or replaces the payer account for a document.
func (e *Engine) SetAccount(document string, balance, dailyLimit Money) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.accounts[document] = &Account{Balance: balance, DailyLimit: dailyLimit, used: map[calendar.Date]Money{}}
}

// Deposit adds funds to a payer account.
func (e *Engine) Deposit(document string, amount Money) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.account(document)
	a.Balance += amount
}

// Balance returns a payer's balance.
func (e *Engine) Balance(document string) Money {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.account(document).Balance
}

func (e *Engine) account(doc string) *Account {
	a, ok := e.accounts[doc]
	if !ok {
		a = &Account{used: map[calendar.Date]Money{}}
		e.accounts[doc] = a
	}
	return a
}

// debit takes money for Pix Automático (limited) or an immediate Pix.
func (e *Engine) debit(doc string, amount Money, automatic bool) error {
	a := e.account(doc)
	d := e.today()
	if automatic && a.DailyLimit > 0 && a.used[d]+amount > a.DailyLimit {
		return errLimit
	}
	if a.Balance < amount {
		return errInsufficient
	}
	a.Balance -= amount
	if automatic {
		a.used[d] += amount
	}
	return nil
}

// RecRequest is what the receiver sends to create a recurrence.
type RecRequest struct {
	Contract, Object    string
	Debtor              Debtor
	Receiver            string
	Start, End          calendar.Date
	Periodicity         calendar.Periodicity
	FixedValue          Money
	ReceiverFloor       Money
	RetryPolicy         RetryPolicy
	AdjustToBusinessDay bool
	Journey             Journey
}

func validDoc(s string) bool {
	if len(s) != 11 && len(s) != 14 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// CreateRecurrence creates a recurrence in status CRIADA.
func (e *Engine) CreateRecurrence(r RecRequest) (*Recurrence, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case strings.TrimSpace(r.Contract) == "":
		return nil, recErr("vinculo.contrato é obrigatório")
	case !validDoc(r.Debtor.Document):
		return nil, recErr("vinculo.devedor deve ter CPF ou CNPJ")
	case !r.Periodicity.Valid():
		return nil, recErr("calendario.periodicidade inválida")
	case r.Start.IsZero() || r.Start.Before(e.today()):
		return nil, recErr("calendario.dataInicial deve ser hoje ou futura")
	case !r.End.IsZero() && !r.Start.Before(r.End):
		return nil, recErr("calendario.dataFinal deve ser posterior à dataInicial")
	case r.FixedValue < 0 || r.ReceiverFloor < 0:
		return nil, recErr("valores não podem ser negativos")
	case r.FixedValue > 0 && r.ReceiverFloor > 0:
		return nil, recErr("valorMinimoRecebedor só se aplica a recorrências de valor variável")
	case r.Journey < Journey1 || r.Journey > Journey4:
		return nil, recErr("jornada deve ser 1, 2, 3 ou 4")
	}
	if r.RetryPolicy == "" {
		r.RetryPolicy = NoRetries
	}
	if r.RetryPolicy != NoRetries && r.RetryPolicy != Retries3R7D {
		return nil, recErr("politicaRetentativa inválida")
	}
	for _, x := range e.recs {
		if x.Contract == r.Contract && x.Debtor.Document == r.Debtor.Document &&
			(x.Status == RecCreated || x.Status == RecApproved) {
			return nil, recErr("já existe recorrência ativa para o contrato %s", r.Contract)
		}
	}
	rec := &Recurrence{ID: e.id("RR"), Contract: r.Contract, Object: r.Object, Debtor: r.Debtor, Receiver: r.Receiver,
		Start: r.Start, End: r.End, Periodicity: r.Periodicity, FixedValue: r.FixedValue, ReceiverFloor: r.ReceiverFloor,
		RetryPolicy: r.RetryPolicy, AdjustToBusinessDay: r.AdjustToBusinessDay, Journey: r.Journey,
		Status: RecCreated, CreatedAt: e.now, UpdatedAt: e.now}
	e.recs[rec.ID] = rec
	val := "variável"
	if !rec.Variable() {
		val = rec.FixedValue.String()
	}
	e.emit("rec.criada", rec.ID, "jornada %d %s %s %s", rec.Journey, rec.Periodicity, val, rec.RetryPolicy)
	cp := *rec
	return &cp, nil
}

// Recurrence returns a copy of a recurrence.
func (e *Engine) Recurrence(id string) (Recurrence, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[id]
	if !ok {
		return Recurrence{}, notFound("recorrência", id)
	}
	return *r, nil
}

// ReviseStart changes the first payment date, allowed only while the
// recurrence awaits confirmation. In Journey 1 the active confirmation
// request must be cancelled first (motive ERSL).
func (e *Engine) ReviseStart(recID string, start calendar.Date) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[recID]
	if !ok {
		return notFound("recorrência", recID)
	}
	if r.Status != RecCreated {
		return recErr("dataInicial só pode ser alterada antes da confirmação")
	}
	if start.Before(e.today()) || (!r.End.IsZero() && !start.Before(r.End)) {
		return recErr("dataInicial inválida")
	}
	if s := e.activeSolic(recID); s != nil {
		return recErr("cancele a solicitação %s (ERSL) antes de alterar a recorrência", s.ID)
	}
	r.Start = start
	r.Revision++
	r.UpdatedAt = e.now
	e.emit("rec.revisada", r.ID, "dataInicial %s", start)
	return nil
}

func (e *Engine) activeSolic(recID string) *Solicitation {
	for _, s := range e.solics {
		if s.RecID == recID && s.Status == SolicCreated {
			return s
		}
	}
	return nil
}

// RequestConfirmation sends a Journey 1 confirmation request (pain.009).
func (e *Engine) RequestConfirmation(recID string, expiry time.Time) (*Solicitation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[recID]
	if !ok {
		return nil, notFound("recorrência", recID)
	}
	if r.Journey != Journey1 {
		return nil, solicErr("solicitação de confirmação é exclusiva da jornada 1")
	}
	if r.Status != RecCreated {
		return nil, solicErr("recorrência não está aguardando confirmação")
	}
	if !expiry.After(e.now) || expiry.Sub(e.now) > MaxSolicitationDays*24*time.Hour {
		return nil, solicErr("expiração deve ser futura e em até %d dias", MaxSolicitationDays)
	}
	if s := e.activeSolic(recID); s != nil {
		return nil, solicErr("já existe solicitação ativa %s para a recorrência", s.ID)
	}
	for _, s := range e.solics {
		if s.RecID == recID && s.Revision == r.Revision && (s.Status == SolicExpired || s.Status == SolicRejected) &&
			e.now.Sub(s.Ended) < MaxSolicitationDays*24*time.Hour {
			return nil, solicErr("oferta idêntica não confirmada há menos de %d dias; revise a recorrência", MaxSolicitationDays)
		}
	}
	s := &Solicitation{ID: e.id("SC"), RecID: recID, Revision: r.Revision, Expiry: expiry.In(calendar.BRT), Status: SolicCreated, Created: e.now}
	e.solics[s.ID] = s
	e.emit("solicrec.enviada", s.ID, "rec %s expira %s", recID, s.Expiry.Format("2006-01-02 15:04"))
	cp := *s
	return &cp, nil
}

// CancelSolicitation cancels a pending confirmation request (pain.011).
// Only the receiver can; the payer accepts or rejects.
func (e *Engine) CancelSolicitation(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.solics[id]
	if !ok {
		return notFound("solicitação", id)
	}
	if s.Status != SolicCreated {
		return solicErr("solicitação não está pendente")
	}
	s.Status, s.Ended = SolicCancelled, e.now
	e.emit("solicrec.cancelada", s.ID, "")
	return nil
}

// Solicitation returns a copy of a confirmation request.
func (e *Engine) Solicitation(id string) (Solicitation, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, ok := e.solics[id]
	if !ok {
		return Solicitation{}, notFound("solicitação", id)
	}
	return *s, nil
}

// ApproveOptions are the payer's choices when authorizing.
type ApproveOptions struct {
	// MaxValue is the payer's maximum per charge (variable recurrences).
	MaxValue Money
	// ImmediatePayment is the first, immediate payment of Journey 3. It
	// must succeed for the authorization to exist.
	ImmediatePayment Money
}

// Approve is the payer authorizing the recurrence in their PSP's app,
// through the recurrence's journey.
func (e *Engine) Approve(recID string, o ApproveOptions) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[recID]
	if !ok {
		return notFound("recorrência", recID)
	}
	if r.Status != RecCreated {
		return recErr("recorrência não está aguardando autorização (status %s)", r.Status)
	}
	if o.MaxValue != 0 {
		if !r.Variable() {
			return recErr("valor máximo só se aplica a recorrências de valor variável")
		}
		if o.MaxValue < r.ReceiverFloor {
			return recErr("valor máximo abaixo do piso do recebedor (%s)", r.ReceiverFloor)
		}
	}
	var solic *Solicitation
	switch r.Journey {
	case Journey1:
		if solic = e.activeSolic(recID); solic == nil {
			return recErr("jornada 1 exige solicitação de confirmação pendente")
		}
	case Journey3:
		if o.ImmediatePayment <= 0 {
			return recErr("jornada 3 exige o pagamento imediato")
		}
		if err := e.debit(r.Debtor.Document, o.ImmediatePayment, false); err != nil {
			e.emit("pix.imediato.recusado", r.ID, "%s %v", o.ImmediatePayment, err)
			return &Error{Type: ErrPaymentFailed.Type, Detail: "pagamento imediato não realizado (" + err.Error() + "); autorização não concedida"}
		}
		e.emit("pix.imediato.liquidado", r.ID, "%s", o.ImmediatePayment)
	}
	if solic != nil {
		solic.Status, solic.Ended = SolicAccepted, e.now
		e.emit("solicrec.aceita", solic.ID, "")
	}
	r.Status, r.PayerMax, r.UpdatedAt = RecApproved, o.MaxValue, e.now
	detail := ""
	if o.MaxValue > 0 {
		detail = "valor máximo " + o.MaxValue.String()
	}
	e.emit("rec.aprovada", r.ID, "%s", detail)
	return nil
}

// Reject is the payer refusing the authorization.
func (e *Engine) Reject(recID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[recID]
	if !ok {
		return notFound("recorrência", recID)
	}
	if r.Status != RecCreated {
		return recErr("recorrência não está aguardando autorização")
	}
	if s := e.activeSolic(recID); s != nil {
		s.Status, s.Ended = SolicRejected, e.now
		e.emit("solicrec.rejeitada", s.ID, "")
	}
	r.Status, r.UpdatedAt = RecRejected, e.now
	e.emit("rec.rejeitada", r.ID, "")
	return nil
}

// SetMaxValue lets the payer change the maximum value later (for instance
// after a charge was refused for exceeding it). Zero removes it.
func (e *Engine) SetMaxValue(recID string, max Money) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[recID]
	if !ok {
		return notFound("recorrência", recID)
	}
	if r.Status != RecApproved || !r.Variable() {
		return recErr("valor máximo só pode ser alterado em autorização ativa de valor variável")
	}
	if max != 0 && max < r.ReceiverFloor {
		return recErr("valor máximo abaixo do piso do recebedor (%s)", r.ReceiverFloor)
	}
	r.PayerMax, r.UpdatedAt = max, e.now
	e.emit("rec.valormaximo", r.ID, "%s", max)
	return nil
}

// CancelAuthorization cancels the recurrence. It is immediate, but payments
// already due today are kept; scheduled payments are cancelled from D+1, or
// from D+2 when the cancellation arrives at or after 22:00.
func (e *Engine) CancelAuthorization(recID string, by Party, reason string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[recID]
	if !ok {
		return notFound("recorrência", recID)
	}
	if r.Status != RecApproved && r.Status != RecCreated {
		return recErr("recorrência não pode ser cancelada (status %s)", r.Status)
	}
	if r.Status == RecCreated && by == Payer {
		return recErr("o pagador não cancela uma recorrência pendente; ele a rejeita")
	}
	if s := e.activeSolic(recID); s != nil {
		s.Status, s.Ended = SolicCancelled, e.now
		e.emit("solicrec.cancelada", s.ID, "")
	}
	from := e.today().AddDays(1)
	if e.now.Hour() >= LateCancelHour {
		from = e.today().AddDays(2)
	}
	r.Status, r.CancelledBy, r.CancelReason, r.UpdatedAt = RecCancelled, by, reason, e.now
	e.emit("rec.cancelada", r.ID, "por %s: %s; débitos a partir de %s cancelados", by, reason, from)
	for _, c := range e.sortedCharges() {
		if c.RecID != recID || c.Status != ChargeActive {
			continue
		}
		a := c.last()
		if a.Status == AttemptScheduled && !a.Date.Before(from) {
			a.Status = AttemptCancelled
			c.Status, c.Reason = ChargeCancelled, "autorização cancelada"
			e.emit("cobr.cancelada", c.TxID, "autorização cancelada")
		}
	}
	return nil
}

// ChargeRequest is a payment instruction (pain.013) from the receiver.
type ChargeRequest struct {
	RecID   string
	TxID    string // optional; generated when empty
	DueDate calendar.Date
	Amount  Money
}

// ScheduleCharge sends a charge to the payer's PSP. It is checked against
// the scheduling window, the recurrence's period and cycles, fixed value and
// the payer's maximum. A charge above the maximum is returned with status
// REJEITADA (the payer is notified, the authorization stays active); rule
// violations are errors.
func (e *Engine) ScheduleCharge(req ChargeRequest) (*Charge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.recs[req.RecID]
	if !ok {
		return nil, notFound("recorrência", req.RecID)
	}
	if r.Status != RecApproved {
		return nil, chargeErr("recorrência não está aprovada (status %s)", r.Status)
	}
	if req.Amount <= 0 {
		return nil, chargeErr("valor.original deve ser positivo")
	}
	if !r.Variable() && req.Amount != r.FixedValue {
		return nil, chargeErr("recorrência de valor fixo %s", r.FixedValue)
	}
	if req.DueDate.Before(r.Start) || (!r.End.IsZero() && req.DueDate.After(r.End)) {
		return nil, chargeErr("dataDeVencimento fora da vigência da recorrência")
	}
	ahead := e.today().DaysUntil(req.DueDate)
	if ahead < MinScheduleDays || ahead > MaxScheduleDays {
		return nil, chargeErr("agendamento deve ocorrer entre %d e %d dias antes do vencimento (faltam %d)", MinScheduleDays, MaxScheduleDays, ahead)
	}
	cycle := calendar.CycleOf(r.Start, r.Periodicity, req.DueDate)
	for _, c := range e.charges {
		if c.RecID == r.ID && c.Cycle == cycle && c.Status != ChargeRejected && c.Status != ChargeCancelled {
			return nil, chargeErr("já existe cobrança %s para o ciclo %d", c.TxID, cycle)
		}
	}
	txid := req.TxID
	if txid == "" {
		e.seq++
		txid = fmt.Sprintf("AUT%s%021d", e.ispb, e.seq)
	}
	if len(txid) < 26 || len(txid) > 35 {
		return nil, chargeErr("txid deve ter de 26 a 35 caracteres")
	}
	if _, dup := e.charges[txid]; dup {
		return nil, chargeErr("txid %s já utilizado", txid)
	}
	c := &Charge{TxID: txid, RecID: r.ID, Cycle: cycle, DueDate: req.DueDate, Amount: req.Amount, Created: e.now}
	e.charges[txid] = c
	if r.PayerMax > 0 && req.Amount > r.PayerMax {
		c.Status, c.Reason = ChargeRejected, "valor acima do máximo definido pelo pagador"
		e.emit("cobr.rejeitada", c.TxID, "%s acima do máximo %s", c.Amount, r.PayerMax)
		e.emit("notificacao.pagador", r.Debtor.Document, "cobrança de %s acima do seu valor máximo %s: revise o limite ou pague por outro meio", c.Amount, r.PayerMax)
		cp := *c
		return &cp, nil
	}
	c.Status = ChargeActive
	c.Attempts = []Attempt{{Type: Scheduled, Date: req.DueDate, Status: AttemptScheduled}}
	e.emit("cobr.agendada", c.TxID, "%s vencimento %s ciclo %d", c.Amount, c.DueDate, cycle)
	if a := e.account(r.Debtor.Document); a.DailyLimit > 0 && req.Amount > a.DailyLimit {
		e.emit("notificacao.pagador", r.Debtor.Document, "agendamento de %s acima do seu limite diário do Pix Automático %s", c.Amount, a.DailyLimit)
	}
	cp := *c
	cp.Attempts = append([]Attempt(nil), c.Attempts...)
	return &cp, nil
}

// Charge returns a copy of a charge.
func (e *Engine) Charge(txid string) (Charge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.charges[txid]
	if !ok {
		return Charge{}, notFound("cobrança", txid)
	}
	cp := *c
	cp.Attempts = append([]Attempt(nil), c.Attempts...)
	return cp, nil
}

// RequestRetry asks for a new attempt (NTAG) on date after a failed one.
// The recurrence must allow retries, the date must be after the due date,
// within seven days of it, requested by 23:59 of the day before, and no
// more than three retry dates may be used.
func (e *Engine) RequestRetry(txid string, date calendar.Date) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.charges[txid]
	if !ok {
		return notFound("cobrança", txid)
	}
	r := e.recs[c.RecID]
	switch {
	case r.RetryPolicy != Retries3R7D:
		return chargeErr("recorrência não permite novas tentativas")
	case r.Status != RecApproved:
		return chargeErr("autorização não está ativa")
	case c.Status != ChargeActive || c.last().Status != AttemptFailed:
		return chargeErr("nova tentativa só após tentativa não liquidada")
	case !date.After(c.DueDate) || date.After(c.DueDate.AddDays(RetryDays)):
		return chargeErr("nova tentativa deve ser em até %d dias após o vencimento", RetryDays)
	case !date.After(e.today()):
		return chargeErr("nova tentativa deve ser solicitada até 23:59 do dia anterior")
	}
	n := 0
	for _, a := range c.Attempts {
		if a.Type == Retry {
			n++
		}
	}
	if n >= MaxRetryDates {
		return chargeErr("limite de %d novas tentativas excedido", MaxRetryDates)
	}
	c.Attempts = append(c.Attempts, Attempt{Type: Retry, Date: date, Status: AttemptScheduled})
	e.tried[c.TxID] = 0
	e.emit("cobr.retentativa", c.TxID, "%d/%d em %s", n+1, MaxRetryDates, date)
	return nil
}

// CancelCharge cancels a scheduled charge. The receiver may cancel until the
// eve of the due date (never during retries); the payer may cancel a
// scheduled debit until 23:59 of the day before it. Cancelling a charge
// does not cancel the authorization.
func (e *Engine) CancelCharge(txid string, by Party) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	c, ok := e.charges[txid]
	if !ok {
		return notFound("cobrança", txid)
	}
	if c.Status != ChargeActive {
		return chargeErr("cobrança não está ativa")
	}
	a := c.last()
	if a.Status != AttemptScheduled {
		return chargeErr("não há débito agendado")
	}
	limit := a.Date
	if by == Receiver {
		if a.Type == Retry {
			return chargeErr("o recebedor não pode cancelar durante as novas tentativas")
		}
		limit = c.DueDate
	}
	if !e.today().Before(limit) {
		return chargeErr("cancelamento permitido somente até a véspera da data do débito")
	}
	a.Status = AttemptCancelled
	c.Status, c.Reason = ChargeCancelled, "cancelada pelo "+strings.ToLower(string(by))
	e.emit("cobr.cancelada", c.TxID, "por %s", by)
	return nil
}

func (e *Engine) sortedCharges() []*Charge {
	out := make([]*Charge, 0, len(e.charges))
	for _, c := range e.charges {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TxID < out[j].TxID })
	return out
}

// next returns the earliest pending action at or before limit.
func (e *Engine) next(limit time.Time) (time.Time, func(), bool) {
	var best time.Time
	var act func()
	consider := func(t time.Time, f func()) {
		if t.After(limit) {
			return
		}
		if t.Before(e.now) {
			t = e.now
		}
		if act == nil || t.Before(best) {
			best, act = t, f
		}
	}
	ids := make([]string, 0, len(e.solics))
	for id := range e.solics {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := e.solics[id]
		if s.Status == SolicCreated {
			s := s
			consider(s.Expiry, func() { e.expireSolic(s) })
		}
	}
	for _, c := range e.sortedCharges() {
		c := c
		if c.Status != ChargeActive {
			continue
		}
		a := c.last()
		switch a.Status {
		case AttemptScheduled:
			if e.tried[c.TxID] == 0 {
				consider(a.Date.At(Window1[0], 0, calendar.BRT), func() { e.settle(c, 1) })
			} else {
				consider(a.Date.At(Window2[0], 0, calendar.BRT), func() { e.settle(c, 2) })
			}
		case AttemptFailed:
			r := e.recs[c.RecID]
			deadline := c.DueDate.AddDays(RetryDays+1).At(0, 0, calendar.BRT)
			retries := 0
			for _, x := range c.Attempts {
				if x.Type == Retry {
					retries++
				}
			}
			if r.RetryPolicy != Retries3R7D || r.Status != RecApproved || retries >= MaxRetryDates {
				consider(e.now, func() { e.expireCharge(c, "sem novas tentativas") })
			} else {
				consider(deadline, func() { e.expireCharge(c, "prazo de novas tentativas encerrado") })
			}
		}
	}
	return best, act, act != nil
}

// Advance moves the clock to t, running every settlement window, retry and
// expiry that falls due on the way, in time order.
func (e *Engine) Advance(t time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	t = t.In(calendar.BRT)
	for {
		at, act, ok := e.next(t)
		if !ok {
			break
		}
		e.now = at
		act()
	}
	if t.After(e.now) {
		e.now = t
	}
}

// AdvanceTo is Advance to hh:mm of a date (Brasília time).
func (e *Engine) AdvanceTo(d calendar.Date, hh, mm int) { e.Advance(d.At(hh, mm, calendar.BRT)) }

func (e *Engine) expireSolic(s *Solicitation) {
	s.Status, s.Ended = SolicExpired, e.now
	e.emit("solicrec.expirada", s.ID, "")
}

func (e *Engine) expireCharge(c *Charge, why string) {
	c.Status, c.Reason = ChargeExpired, why
	e.emit("cobr.expirada", c.TxID, "%s", why)
}

func (e *Engine) settle(c *Charge, window int) {
	r := e.recs[c.RecID]
	a := c.last()
	e.tried[c.TxID] = window
	err := e.debit(r.Debtor.Document, c.Amount, true)
	if err == nil {
		a.Status = AttemptSettled
		c.Status, c.Settled = ChargeSettled, e.now
		e.emit("cobr.liquidada", c.TxID, "%s janela %d (%s)", c.Amount, window, a.Type)
		return
	}
	if window == 1 {
		e.emit("cobr.janela1.falhou", c.TxID, "%v", err)
		e.emit("notificacao.pagador", r.Debtor.Document, "débito de %s para %s não realizado (%v); nova tentativa às 18h", c.Amount, r.Receiver, err)
		return
	}
	a.Status, a.Reason = AttemptFailed, err.Error()
	e.tried[c.TxID] = 0
	e.emit("cobr.nao_liquidada", c.TxID, "%s %v", a.Type, err)
}
