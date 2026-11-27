# pix-automatico-go

[![CI](https://github.com/victorcondackdev/pix-automatico-go/actions/workflows/ci.yml/badge.svg)](https://github.com/victorcondackdev/pix-automatico-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/victorcondackdev/pix-automatico-go.svg)](https://pkg.go.dev/github.com/victorcondackdev/pix-automatico-go)

A Pix Automático engine and sandbox for Go: recurrences and the four
authorization journeys, payment instructions with the 2–10 day scheduling
window, the two settlement windows, post-due retries (3 dates in 7 days), the
payer's maximum value and daily limit, and every cancellation cut-off, all on
a simulated clock, with an HTTP API shaped like API Pix v2.

Recurring billing is where Pix Automático gets hard: what happens when the
first window fails, when the payer cancels at 22:15, when the bill is above
the payer's maximum, when a retry is requested for the wrong day. Those rules
live in PDFs and in PSP sandboxes that cannot fast-forward a month. This
engine models **both PSPs** (the receiver's and the payer's) in one process,
so a subscription product can be developed and tested end to end, including
time travel.

Standard library only. Go 1.22+.

```
calendar/   civil dates, Brazilian banking holidays, cycle and due-date arithmetic
automatico/ the engine: recurrences, journeys, scheduling, settlement, retries, cancellations
brcode/     QR payloads ("Pix Copia e Cola") for journeys 2, 3 and 4, CRC16
scenario/   a small script language, golden transcripts, bundled examples
api/        HTTP: /v2/rec, /v2/solicrec, /v2/cobr (API Pix shapes) and /sim/* for payer and clock
cmd/        pix-automatico-sim (HTTP server) and pix-automatico-demo
```

## Quick start

```sh
go run ./cmd/pix-automatico-demo energia
```

```
> approve luz max=120.00
  erro RecOperacaoInvalida: valor máximo abaixo do piso do recebedor (R$ 150,00)
> approve luz max=200.00
  ok
> charge dez luz 2026-12-15 312.45
  ok AUT12345678000000000000000000002 REJEITADA
  · 2026-12-05 09:00 cobr.rejeitada AUT12345678000000000000000000002 R$ 312,45 acima do máximo R$ 200,00
  · 2026-12-05 09:00 notificacao.pagador 98765432100 cobrança de R$ 312,45 acima do seu valor máximo R$ 200,00: ...
> max luz 400.00
> charge dez2 luz 2026-12-15 312.45
  ok AUT12345678000000000000000000003 ATIVA
> advance 2026-12-15T19:00
  · 2026-12-15 00:00 cobr.janela1.falhou AUT12345678000000000000000000003 saldo insuficiente
  · 2026-12-15 00:00 notificacao.pagador 98765432100 débito de R$ 312,45 para Energia Rio não realizado (saldo insuficiente); nova tentativa às 18h
  · 2026-12-15 18:00 cobr.nao_liquidada AUT12345678000000000000000000003 AGND saldo insuficiente
> retry dez2 2026-12-16
  · 2026-12-15 19:00 cobr.retentativa AUT12345678000000000000000000003 1/3 em 2026-12-16
...
> advance 2026-12-18T09:00
  · 2026-12-18 00:00 cobr.liquidada AUT12345678000000000000000000003 R$ 312,45 janela 1 (NTAG)
```

The four bundled scenarios — `streaming` (fixed value, journey 2), `energia`
(variable value, maximum, floor, retries), `academia` (journey 3, weekly, late
cancellation) and `jornada1` (confirmation request lifecycle) — are also the
golden tests of the engine.

## The rules

| Rule | Where it is enforced |
|---|---|
| Periodicities SEMANAL, MENSAL, TRIMESTRAL, SEMESTRAL, ANUAL; cycles restart on the same weekday / day of month (clamped to month end) | `calendar.CycleStart` |
| Optional business-day adjustment (national holidays, Carnival, extra local holidays) that never leaves its own cycle | `calendar.DueDate` |
| Journey 1: confirmation request (pain.009), expiry up to 30 days, one active per recurrence; an identical offer not confirmed cannot be resent for 30 days; the request must be cancelled (ERSL) before revising the recurrence | `RequestConfirmation`, `ReviseStart` |
| Journey 2: QR with the recurrence only. Journey 3: immediate payment and authorization succeed or fail together; the immediate payment is not part of the recurrence. Journey 4: QR of a charge with due date plus the recurrence | `Approve`, `brcode` |
| Payment instruction 2 to 10 calendar days before the due date; one charge per cycle; inside the recurrence's validity; fixed value must match | `ScheduleCharge` |
| Payer maximum value (variable recurrences), never below the receiver's floor; a charge above it is refused (REJEITADA), the payer is notified, the authorization stays active | `Approve`, `SetMaxValue`, `ScheduleCharge` |
| Daily Pix Automático limit, separate from the Pix limit: scheduling above it is allowed with a warning, settlement fails if the limit is short on the day | `ScheduleCharge`, settlement |
| Settlement windows 00:00–08:00 and 18:00–21:00; payer notified after the first fails | `Advance` |
| Retries (NTAG) only with PERMITE_3R_7D, after a failed attempt, within 7 days after the due date, on at most 3 dates, requested by 23:59 of the previous day, same txid | `RequestRetry` |
| Authorization cancellation is immediate; today's debits are kept, scheduled debits cancelled from D+1 (D+2 when cancelled at or after 22:00) | `CancelAuthorization` |
| Receiver may cancel a charge until the eve of the due date, never during retries; payer may cancel a scheduled debit until 23:59 of the day before; cancelling a charge keeps the authorization | `CancelCharge` |

Sources: Regulamento do Pix (Resolução BCB nº 1/2020 as amended for Pix
Automático), the BCB *Guia de implementação do Pix Automático*, the BCB Pix
Automático FAQ for participants and the API Pix v2 specification. Status and
error names follow API Pix (`CRIADA`, `APROVADA`, `ATIVA`, `CONCLUIDA`,
`RecOperacaoInvalida`, `CobrOperacaoInvalida`, ...). Where the specification
leaves room (event names, attempt statuses, identifier layout) the choices
are this project's and are documented in the code.

## HTTP

```sh
go run ./cmd/pix-automatico-sim -start 2026-11-27T10:00
```

| Method | Path | |
|---|---|---|
| POST | `/v2/rec` | create recurrence (API Pix payload: vinculo, calendario, valor, politicaRetentativa, jornada) |
| GET / PATCH | `/v2/rec/{idRec}` | read; revise `calendario.dataInicial`; `{"status":"CANCELADA"}` |
| GET | `/v2/rec/{idRec}/qrcode` | Pix Copia e Cola for journeys 2–4 |
| POST / GET / PATCH | `/v2/solicrec[/{id}]` | journey 1 confirmation requests |
| PUT / POST / GET / PATCH | `/v2/cobr[/{txid}]` | schedule, read, cancel a charge |
| POST | `/v2/cobr/{txid}/retentativa/{data}` | request a retry |
| POST | `/sim/pagador/rec/{idRec}/aprovar` · `rejeitar` · `cancelar` | the payer in their PSP's app (`valorMaximo`, `pagamentoImediato`) |
| PUT | `/sim/pagador/rec/{idRec}/valormaximo` | payer changes the maximum |
| POST | `/sim/pagador/cobr/{txid}/cancelar` | payer cancels a scheduled debit |
| PUT | `/sim/contas/{documento}` | payer balance and daily limit |
| GET / POST | `/sim/relogio` | read / advance the clock (`avancarAte`) |
| GET | `/sim/eventos?desde=n` | event log (webhook feed) |
| POST | `/sim/cenario` | run a script on a fresh engine, get the transcript |

Errors are RFC 7807 problems with API Pix types, e.g.
`https://pix.bcb.gov.br/api/v2/error/CobrOperacaoInvalida`.

## Library use

```go
e := automatico.New(automatico.Config{ISPB: "12345678", Start: calendar.MustParse("2026-11-27").At(10, 0, calendar.BRT)})
e.SetAccount("12345678909", 100_00, 0)

rec, _ := e.CreateRecurrence(automatico.RecRequest{
    Contract: "ASSINATURA-778", Debtor: automatico.Debtor{Document: "12345678909", Name: "Maria"},
    Start: calendar.MustParse("2026-12-10"), Periodicity: calendar.Monthly,
    FixedValue: 39_90, RetryPolicy: automatico.Retries3R7D, Journey: automatico.Journey2,
})
_ = e.Approve(rec.ID, automatico.ApproveOptions{})

e.AdvanceTo(calendar.MustParse("2026-11-30"), 9, 0)
c, _ := e.ScheduleCharge(automatico.ChargeRequest{RecID: rec.ID, DueDate: calendar.MustParse("2026-12-10"), Amount: 39_90})

e.AdvanceTo(calendar.MustParse("2026-12-10"), 9, 0) // runs the 00:00 window
got, _ := e.Charge(c.TxID)                           // CONCLUIDA
```

## Scope

This is a simulator of the arrangement's rules, not an SPI or DICT client:
messages (pain.009/011/012/013/014) are method calls and events, and money
moves between in-memory accounts. Pair it with
[pix-psp-simulator](https://github.com/victorcondackdev/pix-psp-simulator) or
[pix-in-a-box](https://github.com/victorcondackdev/pix-in-a-box) for the
SPI side.

## .NET

[pix-automatico-dotnet](https://github.com/victorcondackdev/pix-automatico-dotnet)
is a port whose transcripts are byte-identical to this engine's.

## License

MIT
