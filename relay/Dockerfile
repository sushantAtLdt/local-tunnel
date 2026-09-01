# --- build stage ---
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY main.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /relay main.go

# --- final stage: no OS, no shell, just the binary ---
FROM scratch
COPY --from=build /relay /relay
EXPOSE 8080 7000
ENTRYPOINT ["/relay"]
CMD ["-public", ":8080", "-control", ":7000"]
