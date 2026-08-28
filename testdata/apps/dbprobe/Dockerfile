FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN go mod tidy
RUN go build -o /dbprobe .

FROM alpine:3.20
COPY --from=build /dbprobe /dbprobe
EXPOSE 8080
CMD ["/dbprobe"]
