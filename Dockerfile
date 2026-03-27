FROM golang:1.25-alpine AS build

WORKDIR /build

COPY . .

RUN go mod download

ENV GOOS=linux
ENV GOARCH=amd64
ENV CGO_ENABLED=0

RUN go build -ldflags="-s -w" -o server .

FROM alpine:3 AS runner

COPY --from=build /build/server /server

CMD ["/server"]