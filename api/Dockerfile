FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cargodesk ./cmd/cargodesk

FROM alpine:3.22
RUN apk add --no-cache tzdata ca-certificates && adduser -D -u 10001 app
COPY --from=build /out/cargodesk /usr/local/bin/cargodesk
USER app
EXPOSE 8080
ENTRYPOINT ["cargodesk"]
CMD ["serve"]
