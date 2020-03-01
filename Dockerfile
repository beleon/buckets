FROM golang:alpine AS bucketsbuilder

WORKDIR /go/src/github.com/sellleon/buckets
COPY main.go .
RUN go build

FROM alpine:latest
WORKDIR /app/
COPY index.html .
COPY --from=bucketsbuilder /go/src/github.com/sellleon/buckets/buckets .

EXPOSE 8080
ENTRYPOINT ["/app/buckets"]
