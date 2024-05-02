FROM golang:alpine as builder
LABEL maintainer="romanchabest55@gmail.com"

RUN apk update && apk add --no-cache git ca-certificates tzdata ffmpeg && update-ca-certificates

WORKDIR $GOPATH/src/mypackage/myapp/
COPY . .

COPY templates /app/templates

RUN go get -d -v
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' -a \
    -o /go/bin/app .

FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

COPY --from=builder /usr/bin/ffmpeg /usr/bin/ffmpeg
COPY --from=builder /usr/lib/* /usr/lib/
COPY --from=builder /lib/* /lib/

COPY --from=builder /go/bin/app /go/bin/app
COPY --from=builder /app/templates /templates
COPY --form=builder /app/static /static

ENTRYPOINT ["/go/bin/app"]

EXPOSE ${PORT}