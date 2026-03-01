FROM golang:1.24 AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/relay ./cmd/relay

FROM gcr.io/distroless/base-debian12:nonroot
WORKDIR /app

COPY --from=build /out/relay /usr/local/bin/relay
VOLUME ["/data"]

ENV SQLITE_PATH=/data/relay.db
EXPOSE 2525 8080

ENTRYPOINT ["/usr/local/bin/relay"]
CMD ["serve"]

