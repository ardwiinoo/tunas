# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/tunas-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tunas-server /tunas-server
USER nonroot
EXPOSE 8080
ENTRYPOINT ["/tunas-server"]
