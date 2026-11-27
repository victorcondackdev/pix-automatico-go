package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/victorcondackdev/pix-automatico-go/automatico"
	"github.com/victorcondackdev/pix-automatico-go/brcode"
	"github.com/victorcondackdev/pix-automatico-go/calendar"
)

func setup(t *testing.T) (*httptest.Server, *automatico.Engine) {
	t.Helper()
	e := automatico.New(automatico.Config{ISPB: "12345678", Start: calendar.MustParse("2026-11-27").At(10, 0, calendar.BRT)})
	ts := httptest.NewServer(&Server{Engine: e, MerchantName: "Streaming SA", MerchantCity: "RIO DE JANEIRO"})
	t.Cleanup(ts.Close)
	return ts, e
}

func call(t *testing.T, ts *httptest.Server, method, path, body string, want int) map[string]any {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, resp.StatusCode, want, raw)
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

const recJSON = `{"vinculo":{"objeto":"Plano Premium","contrato":"ASSINATURA-778","devedor":{"cpf":"12345678909","nome":"Maria Souza"}},
 "calendario":{"dataInicial":"2026-12-10","periodicidade":"MENSAL"},"valor":{"valorRec":"39.90"},
 "politicaRetentativa":"PERMITE_3R_7D","jornada":2}`

func TestFullFlowOverHTTP(t *testing.T) {
	ts, _ := setup(t)
	call(t, ts, "PUT", "/sim/contas/12345678909", `{"saldo":"100.00"}`, 200)
	rec := call(t, ts, "POST", "/v2/rec", recJSON, 201)
	id := rec["idRec"].(string)
	if rec["status"] != "CRIADA" {
		t.Fatalf("%v", rec)
	}
	qr := call(t, ts, "GET", "/v2/rec/"+id+"/qrcode", "", 200)
	fields, err := brcode.Parse(qr["pixCopiaECola"].(string))
	if err != nil || brcode.Journey(fields) != 2 {
		t.Fatalf("QR %v journey %d", err, brcode.Journey(fields))
	}
	call(t, ts, "POST", "/sim/pagador/rec/"+id+"/aprovar", "", 200)
	call(t, ts, "POST", "/sim/relogio", `{"avancarAte":"2026-11-30T09:00:00-03:00"}`, 200)
	c := call(t, ts, "PUT", "/v2/cobr/ASSINATURA778DEZ2026000000001", `{"idRec":"`+id+`","calendario":{"dataDeVencimento":"2026-12-10"},"valor":{"original":"39.90"}}`, 201)
	if c["status"] != "ATIVA" {
		t.Fatalf("%v", c)
	}
	call(t, ts, "POST", "/sim/relogio", `{"avancarAte":"2026-12-10T09:00:00-03:00"}`, 200)
	c = call(t, ts, "GET", "/v2/cobr/ASSINATURA778DEZ2026000000001", "", 200)
	if c["status"] != "CONCLUIDA" {
		t.Fatalf("%v", c)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/sim/eventos", nil)
	resp, _ := http.DefaultClient.Do(req)
	var evs []map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&evs)
	resp.Body.Close()
	if len(evs) != 4 || evs[3]["tipo"] != "cobr.liquidada" {
		t.Fatalf("events %v", evs)
	}
}

func TestProblemsAndPatch(t *testing.T) {
	ts, _ := setup(t)
	p := call(t, ts, "GET", "/v2/rec/nope", "", 404)
	if p["type"] != "https://pix.bcb.gov.br/api/v2/error/NaoEncontrado" || p["title"] != "Não Encontrado" {
		t.Fatalf("%v", p)
	}
	p = call(t, ts, "POST", "/v2/rec", strings.Replace(recJSON, "MENSAL", "DIARIA", 1), 400)
	if p["type"] != "https://pix.bcb.gov.br/api/v2/error/RecOperacaoInvalida" {
		t.Fatalf("%v", p)
	}
	call(t, ts, "POST", "/v2/rec", `{"campo":1}`, 400)
	rec := call(t, ts, "POST", "/v2/rec", recJSON, 201)
	id := rec["idRec"].(string)
	r := call(t, ts, "PATCH", "/v2/rec/"+id, `{"calendario":{"dataInicial":"2026-12-12"}}`, 200)
	if r["calendario"].(map[string]any)["dataInicial"] != "2026-12-12" {
		t.Fatalf("%v", r)
	}
	r = call(t, ts, "PATCH", "/v2/rec/"+id, `{"status":"CANCELADA"}`, 200)
	if r["status"] != "CANCELADA" {
		t.Fatalf("%v", r)
	}
	call(t, ts, "POST", "/sim/relogio", `{"avancarAte":"2020-01-01T00:00:00-03:00"}`, 400)
}

func TestJourney1OverHTTP(t *testing.T) {
	ts, _ := setup(t)
	rec := call(t, ts, "POST", "/v2/rec", strings.Replace(recJSON, `"jornada":2`, `"jornada":1`, 1), 201)
	id := rec["idRec"].(string)
	call(t, ts, "GET", "/v2/rec/"+id+"/qrcode", "", 400)
	s := call(t, ts, "POST", "/v2/solicrec", `{"idRec":"`+id+`","calendario":{"dataExpiracaoSolicitacao":"2026-11-30T10:00:00-03:00"}}`, 201)
	sid := s["idSolicRec"].(string)
	call(t, ts, "POST", "/sim/pagador/rec/"+id+"/aprovar", `{"valorMaximo":""}`, 200)
	s = call(t, ts, "GET", "/v2/solicrec/"+sid, "", 200)
	if s["status"] != "ACEITA" {
		t.Fatalf("%v", s)
	}
}

func TestScenarioEndpoint(t *testing.T) {
	ts, _ := setup(t)
	resp, err := http.Post(ts.URL+"/sim/cenario", "text/plain", strings.NewReader("clock 2026-11-27T10:00\naccount 12345678909 10.00\n"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(b), "> account 12345678909 10.00") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}
