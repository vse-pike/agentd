FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -o /agentd ./cmd/bot

FROM alpine:3.22
RUN apk add --no-cache ca-certificates
COPY --from=build /agentd /agentd
WORKDIR /data
VOLUME /data
ENV DB_PATH=/data/tasks.db
ENTRYPOINT ["/agentd"]
