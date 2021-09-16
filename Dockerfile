FROM golang:alpine AS bucketsbuilder

RUN apk add --no-cache musl-dev gcc
WORKDIR /go/src/github.com/sellleon/buckets
COPY main.go .
RUN go mod init
RUN go build -ldflags "-linkmode external -extldflags -static"

FROM scratch
WORKDIR /app/
COPY index.html .
COPY --from=bucketsbuilder /go/src/github.com/sellleon/buckets/buckets .

EXPOSE 8080
ENTRYPOINT ["/app/buckets"]
