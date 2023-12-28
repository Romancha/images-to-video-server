FROM golang:alpine as builder
LABEL maintainer="romanchabest55@gmail.com"

RUN apk update && apk add --no-cache git ca-certificates tzdata ffmpeg && update-ca-certificates

ENV USER=appuser
ENV UID=10001

RUN adduser \
    --disabled-password \
    --gecos "" \
    --home "/nonexistent" \
    --shell "/sbin/nologin" \
    --no-create-home \
    --uid "${UID}" \
    "${USER}"

WORKDIR $GOPATH/src/mypackage/myapp/
COPY . .

COPY templates /app/templates

RUN go get -d -v
RUN go mod download

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags='-w -s -extldflags "-static"' -a \
    -o /go/bin/app .

# Create /data/video/ directory and give permissions to appuser
RUN mkdir -p /data/video/ && chown -R ${USER}:${USER} /data/video/

FROM scratch

COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

COPY --from=builder /usr/bin/ffmpeg /usr/bin/ffmpeg
COPY --from=builder /usr/lib/* /usr/lib/
COPY --from=builder /lib/* /lib/

COPY --from=builder /go/bin/app /go/bin/app
COPY --from=builder /app/templates /templates
COPY --from=builder --chown=${USER}:${USER} /data/video/ /data/video/

USER appuser:appuser

ENTRYPOINT ["/go/bin/app"]

EXPOSE ${PORT}