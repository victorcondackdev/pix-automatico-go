// Package scenario runs Pix Automático scripts against the engine and
// produces a transcript: every command, its outcome and the events it
// caused. Transcripts are the golden files of this repository's tests and
// the parity vectors of the .NET port.
//
// Script syntax, one command per line ('#' starts a comment):
//
//	clock 2026-11-27T10:00
//	account <doc> <balance> <dailyLimit>
//	deposit <doc> <amount>
//	rec <name> key=value ...   ('_' is a space in objeto, nome, recebedor;
//	                            keys: contrato objeto devedor nome recebedor inicio fim
//	                            periodicidade valor piso retentativa ajuste jornada)
//	revise <rec> <date>
//	solicit <rec> <solicName> <expiry>
//	cancelsolic <solicName>
//	approve <rec> [max=<amount>] [imediato=<amount>]
//	reject <rec>
//	max <rec> <amount>
//	cancelrec <rec> PAGADOR|RECEBEDOR <reason...>
//	charge <chargeName> <rec> <dueDate> <amount>
//	retry <chargeName> <date>
//	cancelcharge <chargeName> PAGADOR|RECEBEDOR
//	advance <YYYY-MM-DDTHH:MM>
//	status <rec|charge name>
package scenario

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/victorcondackdev/pix-automatico-go/automatico"
	"github.com/victorcondackdev/pix-automatico-go/calendar"
)

// ParseMoney parses "39.90" or "39" into centavos.
func ParseMoney(s string) (automatico.Money, error) {
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 2 || whole == "" {
		return 0, fmt.Errorf("valor inválido %q", s)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err1 := strconv.ParseInt(whole, 10, 64)
	f, err2 := strconv.ParseInt(frac, 10, 64)
	if err1 != nil || err2 != nil || w < 0 {
		return 0, fmt.Errorf("valor inválido %q", s)
	}
	return automatico.Money(w*100 + f), nil
}

// ParseTime parses YYYY-MM-DDTHH:MM in Brasília time.
func ParseTime(s string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02T15:04", s, calendar.BRT)
}

type runner struct {
	e       *automatico.Engine
	recs    map[string]string
	solics  map[string]string
	charges map[string]string
	out     strings.Builder
	seen    int
}

// Run executes a script and returns its transcript. Script errors (bad
// syntax) are returned as errors; rule violations are part of the
// transcript.
func Run(script string) (string, error) {
	r := &runner{recs: map[string]string{}, solics: map[string]string{}, charges: map[string]string{}}
	for n, raw := range strings.Split(script, "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		if r.e == nil && !strings.HasPrefix(line, "clock ") {
			return "", fmt.Errorf("line %d: script must start with clock", n+1)
		}
		res, err := r.exec(strings.Fields(line))
		if err != nil {
			return "", fmt.Errorf("line %d: %v", n+1, err)
		}
		fmt.Fprintf(&r.out, "> %s\n  %s\n", line, res)
		evs := r.e.Events()
		for _, ev := range evs[r.seen:] {
			fmt.Fprintf(&r.out, "  · %s\n", ev)
		}
		r.seen = len(evs)
	}
	return r.out.String(), nil
}

func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	var e *automatico.Error
	if errors.As(err, &e) {
		return "erro " + e.Type + ": " + e.Detail
	}
	return "erro " + err.Error()
}

func (r *runner) id(m map[string]string, name, what string) (string, error) {
	id, ok := m[name]
	if !ok {
		return "", fmt.Errorf("%s %q não definido", what, name)
	}
	return id, nil
}

func (r *runner) exec(f []string) (string, error) {
	need := func(n int) error {
		if len(f) < n {
			return fmt.Errorf("%s: argumentos insuficientes", f[0])
		}
		return nil
	}
	switch f[0] {
	case "clock":
		if err := need(2); err != nil {
			return "", err
		}
		t, err := ParseTime(f[1])
		if err != nil {
			return "", err
		}
		if r.e != nil {
			return "", errors.New("clock só pode ser usado uma vez")
		}
		r.e = automatico.New(automatico.Config{ISPB: "12345678", Start: t})
		return "ok", nil
	case "account", "deposit":
		if err := need(3); err != nil {
			return "", err
		}
		a, err := ParseMoney(f[2])
		if err != nil {
			return "", err
		}
		if f[0] == "deposit" {
			r.e.Deposit(f[1], a)
			return "ok saldo " + r.e.Balance(f[1]).String(), nil
		}
		var lim automatico.Money
		if len(f) > 3 {
			if lim, err = ParseMoney(f[3]); err != nil {
				return "", err
			}
		}
		r.e.SetAccount(f[1], a, lim)
		return "ok", nil
	case "rec":
		if err := need(2); err != nil {
			return "", err
		}
		req, err := recRequest(f[2:])
		if err != nil {
			return "", err
		}
		rec, err := r.e.CreateRecurrence(req)
		if err != nil {
			return outcome(err), nil
		}
		r.recs[f[1]] = rec.ID
		return "ok " + rec.ID, nil
	case "revise":
		if err := need(3); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[1], "rec")
		if err != nil {
			return "", err
		}
		dt, err := calendar.Parse(f[2])
		if err != nil {
			return "", err
		}
		return outcome(r.e.ReviseStart(id, dt)), nil
	case "solicit":
		if err := need(4); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[1], "rec")
		if err != nil {
			return "", err
		}
		t, err := ParseTime(f[3])
		if err != nil {
			return "", err
		}
		s, err := r.e.RequestConfirmation(id, t)
		if err != nil {
			return outcome(err), nil
		}
		r.solics[f[2]] = s.ID
		return "ok " + s.ID, nil
	case "cancelsolic":
		if err := need(2); err != nil {
			return "", err
		}
		id, err := r.id(r.solics, f[1], "solicitação")
		if err != nil {
			return "", err
		}
		return outcome(r.e.CancelSolicitation(id)), nil
	case "approve":
		if err := need(2); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[1], "rec")
		if err != nil {
			return "", err
		}
		var o automatico.ApproveOptions
		for _, kv := range f[2:] {
			k, v, _ := strings.Cut(kv, "=")
			m, err := ParseMoney(v)
			if err != nil {
				return "", err
			}
			switch k {
			case "max":
				o.MaxValue = m
			case "imediato":
				o.ImmediatePayment = m
			default:
				return "", fmt.Errorf("approve: opção %q", k)
			}
		}
		return outcome(r.e.Approve(id, o)), nil
	case "reject":
		if err := need(2); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[1], "rec")
		if err != nil {
			return "", err
		}
		return outcome(r.e.Reject(id)), nil
	case "max":
		if err := need(3); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[1], "rec")
		if err != nil {
			return "", err
		}
		m, err := ParseMoney(f[2])
		if err != nil {
			return "", err
		}
		return outcome(r.e.SetMaxValue(id, m)), nil
	case "cancelrec":
		if err := need(4); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[1], "rec")
		if err != nil {
			return "", err
		}
		return outcome(r.e.CancelAuthorization(id, automatico.Party(f[2]), strings.Join(f[3:], " "))), nil
	case "charge":
		if err := need(5); err != nil {
			return "", err
		}
		id, err := r.id(r.recs, f[2], "rec")
		if err != nil {
			return "", err
		}
		dt, err := calendar.Parse(f[3])
		if err != nil {
			return "", err
		}
		m, err := ParseMoney(f[4])
		if err != nil {
			return "", err
		}
		c, err := r.e.ScheduleCharge(automatico.ChargeRequest{RecID: id, DueDate: dt, Amount: m})
		if err != nil {
			return outcome(err), nil
		}
		r.charges[f[1]] = c.TxID
		return "ok " + c.TxID + " " + string(c.Status), nil
	case "retry":
		if err := need(3); err != nil {
			return "", err
		}
		id, err := r.id(r.charges, f[1], "cobrança")
		if err != nil {
			return "", err
		}
		dt, err := calendar.Parse(f[2])
		if err != nil {
			return "", err
		}
		return outcome(r.e.RequestRetry(id, dt)), nil
	case "cancelcharge":
		if err := need(3); err != nil {
			return "", err
		}
		id, err := r.id(r.charges, f[1], "cobrança")
		if err != nil {
			return "", err
		}
		return outcome(r.e.CancelCharge(id, automatico.Party(f[2]))), nil
	case "advance":
		if err := need(2); err != nil {
			return "", err
		}
		t, err := ParseTime(f[1])
		if err != nil {
			return "", err
		}
		r.e.Advance(t)
		return "ok " + r.e.Now().Format("2006-01-02 15:04"), nil
	case "status":
		if err := need(2); err != nil {
			return "", err
		}
		if id, ok := r.recs[f[1]]; ok {
			x, _ := r.e.Recurrence(id)
			return fmt.Sprintf("rec %s max %s", x.Status, x.PayerMax), nil
		}
		if id, ok := r.charges[f[1]]; ok {
			c, _ := r.e.Charge(id)
			parts := []string{"cobr " + string(c.Status)}
			for _, a := range c.Attempts {
				parts = append(parts, fmt.Sprintf("%s:%s:%s", a.Type, a.Date, a.Status))
			}
			return strings.Join(parts, " "), nil
		}
		return "", fmt.Errorf("status: %q não definido", f[1])
	}
	return "", fmt.Errorf("comando desconhecido %q", f[0])
}

func recRequest(kvs []string) (automatico.RecRequest, error) {
	req := automatico.RecRequest{Journey: automatico.Journey2, Periodicity: calendar.Monthly}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return req, fmt.Errorf("rec: esperado chave=valor em %q", kv)
		}
		text := strings.ReplaceAll(v, "_", " ") // free text: '_' stands for a space
		var err error
		switch k {
		case "contrato":
			req.Contract = v
		case "objeto":
			req.Object = text
		case "devedor":
			req.Debtor.Document = v
		case "nome":
			req.Debtor.Name = text
		case "recebedor":
			req.Receiver = text
		case "inicio":
			req.Start, err = calendar.Parse(v)
		case "fim":
			req.End, err = calendar.Parse(v)
		case "periodicidade":
			req.Periodicity = calendar.Periodicity(v)
		case "valor":
			req.FixedValue, err = ParseMoney(v)
		case "piso":
			req.ReceiverFloor, err = ParseMoney(v)
		case "retentativa":
			req.RetryPolicy = automatico.RetryPolicy(v)
		case "ajuste":
			req.AdjustToBusinessDay = v == "sim"
		case "jornada":
			var j int
			j, err = strconv.Atoi(v)
			req.Journey = automatico.Journey(j)
		default:
			return req, fmt.Errorf("rec: chave desconhecida %q", k)
		}
		if err != nil {
			return req, err
		}
	}
	return req, nil
}
