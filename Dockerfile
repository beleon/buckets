FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod main.go index.html ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /buckets .

FROM scratch
COPY --from=build /buckets /buckets
USER 65534:65534
EXPOSE 8080
ENTRYPOINT ["/buckets"]
