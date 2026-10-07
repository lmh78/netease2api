FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /netease2api .

FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -S app && adduser -S -G app app && mkdir /data && chown app:app /data
COPY --from=build /netease2api /usr/local/bin/netease2api
USER app
EXPOSE 18080
VOLUME ["/data"]
ENTRYPOINT ["netease2api"]
CMD ["-addr", "0.0.0.0:18080", "-data", "/data"]
