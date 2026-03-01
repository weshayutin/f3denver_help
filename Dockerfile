FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build
ARG TARGETARCH
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOARCH=${TARGETARCH} go build -o /f3denver_help .

FROM alpine:latest
RUN apk add --no-cache ca-certificates tzdata sqlite-libs
RUN mkdir -p /data
WORKDIR /app
COPY --from=build /f3denver_help .
COPY --from=build /src/templates ./templates
COPY --from=build /src/static ./static
COPY --from=build /src/data ./data
ENV DATA_DIR=/data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["./f3denver_help"]
