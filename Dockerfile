# syntax=docker/dockerfile:1

# 1. Интерфейс: React → web/dist/app
FROM node:24-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2. Snag: один статический бинарник с вшитым интерфейсом
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist/app ./web/dist/app
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/snag ./cmd/snag \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/snag-bench ./cmd/snag-bench

# 3. Итоговый образ без оболочки и пакетного менеджера, от непривилегированного пользователя
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/snag /out/snag-bench /usr/local/bin/
EXPOSE 8000
ENV SNAG_ADDR=:8000
ENTRYPOINT ["/usr/local/bin/snag"]
CMD ["serve"]
