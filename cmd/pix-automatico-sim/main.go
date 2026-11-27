// Command pix-automatico-sim serves the Pix Automático engine over HTTP:
// API Pix v2 shaped /v2/rec, /v2/solicrec and /v2/cobr for the receiver, and
// /sim/... endpoints for the payer's app, accounts and the clock.
//
//	pix-automatico-sim -addr :8080 -start 2026-11-27T10:00
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/victorcondackdev/pix-automatico-go/api"
	"github.com/victorcondackdev/pix-automatico-go/automatico"
	"github.com/victorcondackdev/pix-automatico-go/calendar"
	"github.com/victorcondackdev/pix-automatico-go/scenario"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "pix-automatico-sim:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("pix-automatico-sim", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	start := fs.String("start", "", "simulated start time YYYY-MM-DDTHH:MM (Brasília); default now")
	ispb := fs.String("ispb", "12345678", "receiver PSP ISPB used in identifiers")
	name := fs.String("merchant", "Recebedor Exemplo", "merchant name in QR codes")
	city := fs.String("city", "RIO DE JANEIRO", "merchant city in QR codes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	t := time.Now().In(calendar.BRT).Truncate(time.Minute)
	if *start != "" {
		var err error
		if t, err = scenario.ParseTime(*start); err != nil {
			return fmt.Errorf("-start: %w", err)
		}
	}
	e := automatico.New(automatico.Config{ISPB: *ispb, Start: t})
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	srv := &http.Server{Addr: *addr, ReadHeaderTimeout: 5 * time.Second,
		Handler: logRequests(log, &api.Server{Engine: e, MerchantName: *name, MerchantCity: *city})}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		sh, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sh)
	}()
	log.Info("pix-automatico-sim listening", "addr", *addr, "clock", t.Format(time.RFC3339))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }

func logRequests(log *slog.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &recorder{ResponseWriter: w, status: 200}
		start := time.Now()
		h.ServeHTTP(rec, r)
		log.Info("http", "method", r.Method, "path", r.URL.Path, "status", rec.status, "took", time.Since(start))
	})
}
