# Use a multi-stage build
FROM golang:alpine AS builder
RUN apk update

WORKDIR /app
COPY . .

# If you want to build all main.go files in cmd directory
# RUN for file in $(find cmd -name "main.go"); do \
#     dir=$(dirname "$file"); \
#     name=$(basename "$dir"); \
#     go build -o bin/$name $file; \
#   done

RUN go build -o bin/controller cmd/controller/main.go && \
    go build -o bin/collector cmd/collector/main.go && \
    go build -o bin/actuator cmd/actuator/main.go && \
    go build -o bin/loademulator cmd/loademulator/main.go

# Create the final image
FROM alpine
RUN apk update
COPY --from=builder /app/bin /bin

# Expose the port the API will listen on
EXPOSE 8080

# Command to run the binary when the container starts
CMD ["controller"]