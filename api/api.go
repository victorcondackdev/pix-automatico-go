// Package api exposes the engine over HTTP with API Pix v2 shapes for the
// receiver's side (/v2/rec, /v2/solicrec, /v2/cobr) and simulator endpoints
// for what a real PSP would do in the payer's app or in its back office
// (/sim/...). Errors are RFC 7807 problems with API Pix error types.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/victorcondackdev/pix-automatico-go/automatico"
	"github.com/victorcondackdev/pix-automatico-go/brcode"
	"github.com/victorcondackdev/pix-automatico-go/calendar"
	"github.com/victorcondackdev/pix-automatico-go/scenario"
)

// Server is an http.Handler over an engine.
type Server struct {
	Engine *automatico.Engine
	// BaseURL is the host and path prefix used in QR code locations, e.g.
	// "pix.example.com/qr/v2". Defaults to "pix-automatico.local/qr/v2".
	BaseURL string
	// MerchantName and MerchantCity go into generated QR codes.
	MerchantName, MerchantCity string
	mux                        *http.ServeMux
}

// Handler returns the routed handler.
func (s *Server) Handler() http.Handler {
	if s.mux != nil {
		return s.mux
	}
	m := http.NewServeMux()
	m.HandleFunc("POST /v2/rec", s.createRec)
	m.HandleFunc("GET /v2/rec/{idRec}", s.getRec)
	m.HandleFunc("PATCH /v2/rec/{idRec}", s.patchRec)
	m.HandleFunc("GET /v2/rec/{idRec}/qrcode", s.qrcode)
	m.HandleFunc("POST /v2/solicrec", s.createSolic)
	m.HandleFunc("GET /v2/solicrec/{id}", s.getSolic)
	m.HandleFunc("PATCH /v2/solicrec/{id}", s.patchSolic)
	m.HandleFunc("PUT /v2/cobr/{txid}", s.putCobr)
	m.HandleFunc("POST /v2/cobr", s.putCobr)
	m.HandleFunc("GET /v2/cobr/{txid}", s.getCobr)
	m.HandleFunc("PATCH /v2/cobr/{txid}", s.patchCobr)
	m.HandleFunc("POST /v2/cobr/{txid}/retentativa/{data}", s.retry)
	m.HandleFunc("POST /sim/pagador/rec/{idRec}/aprovar", s.approve)
	m.HandleFunc("POST /sim/pagador/rec/{idRec}/rejeitar", s.reject)
	m.HandleFunc("POST /sim/pagador/rec/{idRec}/cancelar", s.payerCancel)
	m.HandleFunc("PUT /sim/pagador/rec/{idRec}/valormaximo", s.setMax)
	m.HandleFunc("POST /sim/pagador/cobr/{txid}/cancelar", s.payerCancelCharge)
	m.HandleFunc("PUT /sim/contas/{documento}", s.putAccount)
	m.HandleFunc("POST /sim/relogio", s.advance)
	m.HandleFunc("GET /sim/relogio", s.clock)
	m.HandleFunc("GET /sim/eventos", s.events)
	m.HandleFunc("POST /sim/cenario", s.runScenario)
	s.mux = m
	return m
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.Handler().ServeHTTP(w, r) }

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

var titles = map[string]string{
	"NaoEncontrado":            "Não Encontrado",
	"RecOperacaoInvalida":      "Operação inválida.",
	"SolicRecOperacaoInvalida": "Operação inválida.",
	"CobrOperacaoInvalida":     "Operação inválida.",
	"PagamentoNaoRealizado":    "Pagamento não realizado.",
	"RequisicaoInvalida":       "Requisição inválida.",
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	var e *automatico.Error
	p := problem{Type: "RequisicaoInvalida", Status: 400, Detail: err.Error()}
	if errors.As(err, &e) {
		p.Type, p.Detail = e.Type, e.Detail
		if e.Type == "NaoEncontrado" {
			p.Status = 404
		}
	}
	p.Title = titles[p.Type]
	p.Type = "https://pix.bcb.gov.br/api/v2/error/" + p.Type
	w.Header().Set("Content-Type", "application/problem+json; charset=utf-8")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

func bad(format string, a ...any) error { return fmt.Errorf(format, a...) }

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return bad("JSON inválido: %v", err)
	}
	return nil
}

// recBody mirrors the API Pix /rec payload (subset).
type recBody struct {
	Vinculo struct {
		Objeto   string `json:"objeto"`
		Contrato string `json:"contrato"`
		Devedor  struct {
			CPF  string `json:"cpf,omitempty"`
			CNPJ string `json:"cnpj,omitempty"`
			Nome string `json:"nome"`
		} `json:"devedor"`
	} `json:"vinculo"`
	Calendario struct {
		DataInicial   string `json:"dataInicial"`
		DataFinal     string `json:"dataFinal,omitempty"`
		Periodicidade string `json:"periodicidade"`
	} `json:"calendario"`
	Valor struct {
		ValorRec             string `json:"valorRec,omitempty"`
		ValorMinimoRecebedor string `json:"valorMinimoRecebedor,omitempty"`
	} `json:"valor"`
	PoliticaRetentativa string `json:"politicaRetentativa,omitempty"`
	AjusteDiaUtil       bool   `json:"ajusteDiaUtil,omitempty"`
	Recebedor           string `json:"recebedor,omitempty"`
	Jornada             int    `json:"jornada"`
}

type recView struct {
	IDRec   string `json:"idRec"`
	Status  string `json:"status"`
	Jornada int    `json:"jornada"`
	Vinculo struct {
		Objeto   string            `json:"objeto,omitempty"`
		Contrato string            `json:"contrato"`
		Devedor  map[string]string `json:"devedor"`
	} `json:"vinculo"`
	Calendario struct {
		DataInicial   string `json:"dataInicial"`
		DataFinal     string `json:"dataFinal,omitempty"`
		Periodicidade string `json:"periodicidade"`
	} `json:"calendario"`
	Valor struct {
		ValorRec             string `json:"valorRec,omitempty"`
		ValorMinimoRecebedor string `json:"valorMinimoRecebedor,omitempty"`
		ValorMaximoPagador   string `json:"valorMaximoPagador,omitempty"`
	} `json:"valor"`
	PoliticaRetentativa string `json:"politicaRetentativa"`
	AjusteDiaUtil       bool   `json:"ajusteDiaUtil"`
	Atualizacao         string `json:"atualizacao"`
}

func money(m automatico.Money) string {
	if m == 0 {
		return ""
	}
	return m.Decimal()
}

func viewRec(r automatico.Recurrence) recView {
	var v recView
	v.IDRec, v.Status, v.Jornada = r.ID, string(r.Status), int(r.Journey)
	v.Vinculo.Objeto, v.Vinculo.Contrato = r.Object, r.Contract
	doc := "cpf"
	if len(r.Debtor.Document) == 14 {
		doc = "cnpj"
	}
	v.Vinculo.Devedor = map[string]string{doc: r.Debtor.Document, "nome": r.Debtor.Name}
	v.Calendario.DataInicial, v.Calendario.Periodicidade = r.Start.String(), string(r.Periodicity)
	if !r.End.IsZero() {
		v.Calendario.DataFinal = r.End.String()
	}
	v.Valor.ValorRec, v.Valor.ValorMinimoRecebedor, v.Valor.ValorMaximoPagador = money(r.FixedValue), money(r.ReceiverFloor), money(r.PayerMax)
	v.PoliticaRetentativa, v.AjusteDiaUtil = string(r.RetryPolicy), r.AdjustToBusinessDay
	v.Atualizacao = r.UpdatedAt.Format(time.RFC3339)
	return v
}

func optMoney(s, field string) (automatico.Money, error) {
	if s == "" {
		return 0, nil
	}
	m, err := scenario.ParseMoney(s)
	if err != nil {
		return 0, bad("%s inválido", field)
	}
	return m, nil
}

func (s *Server) createRec(w http.ResponseWriter, r *http.Request) {
	var b recBody
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	req := automatico.RecRequest{Contract: b.Vinculo.Contrato, Object: b.Vinculo.Objeto, Receiver: b.Recebedor,
		Periodicity: calendar.Periodicity(b.Calendario.Periodicidade), RetryPolicy: automatico.RetryPolicy(b.PoliticaRetentativa),
		AdjustToBusinessDay: b.AjusteDiaUtil, Journey: automatico.Journey(b.Jornada)}
	req.Debtor.Document = b.Vinculo.Devedor.CPF + b.Vinculo.Devedor.CNPJ
	req.Debtor.Name = b.Vinculo.Devedor.Nome
	var err error
	if req.Start, err = calendar.Parse(b.Calendario.DataInicial); err != nil {
		fail(w, bad("calendario.dataInicial inválida"))
		return
	}
	if b.Calendario.DataFinal != "" {
		if req.End, err = calendar.Parse(b.Calendario.DataFinal); err != nil {
			fail(w, bad("calendario.dataFinal inválida"))
			return
		}
	}
	if req.FixedValue, err = optMoney(b.Valor.ValorRec, "valor.valorRec"); err != nil {
		fail(w, err)
		return
	}
	if req.ReceiverFloor, err = optMoney(b.Valor.ValorMinimoRecebedor, "valor.valorMinimoRecebedor"); err != nil {
		fail(w, err)
		return
	}
	rec, err := s.Engine.CreateRecurrence(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, viewRec(*rec))
}

func (s *Server) getRec(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Engine.Recurrence(r.PathValue("idRec"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, viewRec(rec))
}

// PATCH /v2/rec/{idRec}: {"status":"CANCELADA"} or {"calendario":{"dataInicial":...}}
func (s *Server) patchRec(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status     string `json:"status"`
		Calendario struct {
			DataInicial string `json:"dataInicial"`
		} `json:"calendario"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	id := r.PathValue("idRec")
	var err error
	switch {
	case b.Status == "CANCELADA":
		err = s.Engine.CancelAuthorization(id, automatico.Receiver, "cancelada pelo recebedor")
	case b.Calendario.DataInicial != "":
		var dt calendar.Date
		if dt, err = calendar.Parse(b.Calendario.DataInicial); err == nil {
			err = s.Engine.ReviseStart(id, dt)
		}
	default:
		err = bad("nada a alterar")
	}
	if err != nil {
		fail(w, err)
		return
	}
	s.getRec(w, r)
}

func (s *Server) base() string {
	if s.BaseURL != "" {
		return s.BaseURL
	}
	return "pix-automatico.local/qr/v2"
}

func (s *Server) qrcode(w http.ResponseWriter, r *http.Request) {
	rec, err := s.Engine.Recurrence(r.PathValue("idRec"))
	if err != nil {
		fail(w, err)
		return
	}
	if rec.Journey == automatico.Journey1 {
		fail(w, &automatico.Error{Type: "RecOperacaoInvalida", Detail: "jornada 1 não usa QR code"})
		return
	}
	name, city := s.MerchantName, s.MerchantCity
	if name == "" {
		name = "Recebedor"
	}
	if city == "" {
		city = "BRASILIA"
	}
	p := brcode.Payload{MerchantName: name, MerchantCity: city, RecURL: s.base() + "/rec/" + strings.ToLower(rec.ID)}
	if rec.Journey >= automatico.Journey3 {
		p.CobURL = s.base() + "/cob/" + strings.ToLower(rec.ID)
		p.Amount = money(rec.FixedValue)
	}
	code, err := p.Encode()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"idRec": rec.ID, "jornada": rec.Journey, "pixCopiaECola": code})
}

type solicView struct {
	IDSolicRec string `json:"idSolicRec"`
	IDRec      string `json:"idRec"`
	Status     string `json:"status"`
	Expiracao  string `json:"expiracao"`
}

func viewSolic(x automatico.Solicitation) solicView {
	return solicView{x.ID, x.RecID, string(x.Status), x.Expiry.Format(time.RFC3339)}
}

func (s *Server) createSolic(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDRec      string `json:"idRec"`
		Calendario struct {
			DataExpiracaoSolicitacao string `json:"dataExpiracaoSolicitacao"`
		} `json:"calendario"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	exp, err := time.Parse(time.RFC3339, b.Calendario.DataExpiracaoSolicitacao)
	if err != nil {
		fail(w, bad("calendario.dataExpiracaoSolicitacao deve ser RFC 3339"))
		return
	}
	x, err := s.Engine.RequestConfirmation(b.IDRec, exp)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, viewSolic(*x))
}

func (s *Server) getSolic(w http.ResponseWriter, r *http.Request) {
	x, err := s.Engine.Solicitation(r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, viewSolic(x))
}

func (s *Server) patchSolic(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if b.Status != "CANCELADA" {
		fail(w, bad("somente status CANCELADA é aceito"))
		return
	}
	if err := s.Engine.CancelSolicitation(r.PathValue("id")); err != nil {
		fail(w, err)
		return
	}
	s.getSolic(w, r)
}

type cobrView struct {
	TxID       string `json:"txid"`
	IDRec      string `json:"idRec"`
	Status     string `json:"status"`
	Calendario struct {
		DataDeVencimento string `json:"dataDeVencimento"`
	} `json:"calendario"`
	Valor struct {
		Original string `json:"original"`
	} `json:"valor"`
	Motivo     string `json:"motivo,omitempty"`
	Tentativas []struct {
		Tipo   string `json:"tipo"`
		Data   string `json:"dataLiquidacao"`
		Status string `json:"status"`
		Motivo string `json:"motivo,omitempty"`
	} `json:"tentativas"`
}

func viewCobr(c automatico.Charge) cobrView {
	var v cobrView
	v.TxID, v.IDRec, v.Status, v.Motivo = c.TxID, c.RecID, string(c.Status), c.Reason
	v.Calendario.DataDeVencimento = c.DueDate.String()
	v.Valor.Original = c.Amount.Decimal()
	for _, a := range c.Attempts {
		v.Tentativas = append(v.Tentativas, struct {
			Tipo   string `json:"tipo"`
			Data   string `json:"dataLiquidacao"`
			Status string `json:"status"`
			Motivo string `json:"motivo,omitempty"`
		}{string(a.Type), a.Date.String(), string(a.Status), a.Reason})
	}
	return v
}

func (s *Server) putCobr(w http.ResponseWriter, r *http.Request) {
	var b struct {
		IDRec      string `json:"idRec"`
		Calendario struct {
			DataDeVencimento string `json:"dataDeVencimento"`
		} `json:"calendario"`
		Valor struct {
			Original string `json:"original"`
		} `json:"valor"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	due, err := calendar.Parse(b.Calendario.DataDeVencimento)
	if err != nil {
		fail(w, bad("calendario.dataDeVencimento inválida"))
		return
	}
	amt, err := scenario.ParseMoney(b.Valor.Original)
	if err != nil {
		fail(w, bad("valor.original inválido"))
		return
	}
	c, err := s.Engine.ScheduleCharge(automatico.ChargeRequest{RecID: b.IDRec, TxID: r.PathValue("txid"), DueDate: due, Amount: amt})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 201, viewCobr(*c))
}

func (s *Server) getCobr(w http.ResponseWriter, r *http.Request) {
	c, err := s.Engine.Charge(r.PathValue("txid"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, viewCobr(c))
}

func (s *Server) patchCobr(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Status string `json:"status"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	if b.Status != "CANCELADA" {
		fail(w, bad("somente status CANCELADA é aceito"))
		return
	}
	if err := s.Engine.CancelCharge(r.PathValue("txid"), automatico.Receiver); err != nil {
		fail(w, err)
		return
	}
	s.getCobr(w, r)
}

func (s *Server) retry(w http.ResponseWriter, r *http.Request) {
	dt, err := calendar.Parse(r.PathValue("data"))
	if err != nil {
		fail(w, bad("data inválida"))
		return
	}
	if err := s.Engine.RequestRetry(r.PathValue("txid"), dt); err != nil {
		fail(w, err)
		return
	}
	s.getCobr(w, r)
}

func (s *Server) approve(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ValorMaximo       string `json:"valorMaximo"`
		PagamentoImediato string `json:"pagamentoImediato"`
	}
	if r.ContentLength != 0 {
		if err := decode(r, &b); err != nil {
			fail(w, err)
			return
		}
	}
	var o automatico.ApproveOptions
	var err error
	if o.MaxValue, err = optMoney(b.ValorMaximo, "valorMaximo"); err != nil {
		fail(w, err)
		return
	}
	if o.ImmediatePayment, err = optMoney(b.PagamentoImediato, "pagamentoImediato"); err != nil {
		fail(w, err)
		return
	}
	if err := s.Engine.Approve(r.PathValue("idRec"), o); err != nil {
		fail(w, err)
		return
	}
	s.getRec(w, r)
}

func (s *Server) reject(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.Reject(r.PathValue("idRec")); err != nil {
		fail(w, err)
		return
	}
	s.getRec(w, r)
}

func (s *Server) payerCancel(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.CancelAuthorization(r.PathValue("idRec"), automatico.Payer, "cancelada pelo pagador"); err != nil {
		fail(w, err)
		return
	}
	s.getRec(w, r)
}

func (s *Server) setMax(w http.ResponseWriter, r *http.Request) {
	var b struct {
		ValorMaximo string `json:"valorMaximo"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	m, err := optMoney(b.ValorMaximo, "valorMaximo")
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.Engine.SetMaxValue(r.PathValue("idRec"), m); err != nil {
		fail(w, err)
		return
	}
	s.getRec(w, r)
}

func (s *Server) payerCancelCharge(w http.ResponseWriter, r *http.Request) {
	if err := s.Engine.CancelCharge(r.PathValue("txid"), automatico.Payer); err != nil {
		fail(w, err)
		return
	}
	s.getCobr(w, r)
}

func (s *Server) putAccount(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Saldo        string `json:"saldo"`
		LimiteDiario string `json:"limiteDiario"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	bal, err := optMoney(b.Saldo, "saldo")
	if err != nil {
		fail(w, err)
		return
	}
	lim, err := optMoney(b.LimiteDiario, "limiteDiario")
	if err != nil {
		fail(w, err)
		return
	}
	s.Engine.SetAccount(r.PathValue("documento"), bal, lim)
	writeJSON(w, 200, map[string]string{"documento": r.PathValue("documento"), "saldo": bal.Decimal()})
}

func (s *Server) clock(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, map[string]string{"agora": s.Engine.Now().Format(time.RFC3339)})
}

func (s *Server) advance(w http.ResponseWriter, r *http.Request) {
	var b struct {
		AvancarAte string `json:"avancarAte"`
	}
	if err := decode(r, &b); err != nil {
		fail(w, err)
		return
	}
	t, err := time.Parse(time.RFC3339, b.AvancarAte)
	if err != nil {
		fail(w, bad("avancarAte deve ser RFC 3339"))
		return
	}
	if t.Before(s.Engine.Now()) {
		fail(w, bad("o relógio não volta"))
		return
	}
	s.Engine.Advance(t)
	s.clock(w, r)
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	evs := s.Engine.Events()
	from := 0
	if v := r.URL.Query().Get("desde"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= len(evs) {
			from = n
		}
	}
	type ev struct {
		Seq     int    `json:"seq"`
		Horario string `json:"horario"`
		Tipo    string `json:"tipo"`
		Ref     string `json:"ref"`
		Detalhe string `json:"detalhe,omitempty"`
	}
	out := []ev{}
	for i, e := range evs[from:] {
		out = append(out, ev{from + i, e.At.Format(time.RFC3339), e.Kind, e.Ref, e.Detail})
	}
	writeJSON(w, 200, out)
}

// POST /sim/cenario runs a script on a fresh engine (text/plain body) and
// returns the transcript. It does not touch the server's engine.
func (s *Server) runScenario(w http.ResponseWriter, r *http.Request) {
	src, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, bad("corpo inválido: %v", err))
		return
	}
	out, err := scenario.Run(string(src))
	if err != nil {
		fail(w, bad("%v", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(out))
}
