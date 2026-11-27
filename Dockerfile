# syntax=docker/dockerfile:1
FROM golang:1.22 AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/pix-automatico-sim ./cmd/pix-automatico-sim

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/pix-automatico-sim /pix-automatico-sim
EXPOSE 8080
ENTRYPOINT ["/pix-automatico-sim", "-addr", ":8080"]
